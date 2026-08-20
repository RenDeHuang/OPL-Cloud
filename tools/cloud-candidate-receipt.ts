import { createHash } from "node:crypto";
import { chmod, lstat, readFile, readdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const SHA = /^[0-9a-f]{40}$/;
const DIGEST = /^sha256:[0-9a-f]{64}$/;
const SHA256 = /^[0-9a-f]{64}$/;
const POSITIVE_DECIMAL = /^[1-9][0-9]*$/;
const PRODUCT_REPOSITORY = "gaofeng21cn/one-person-lab-cloud";
const CLOUD_IMAGE_REPOSITORY = "ghcr.io/gaofeng21cn/one-person-lab-cloud";
const MANIFEST_NAME = "opl-cloud-candidate.json";
const CHECKSUM_MANIFEST_NAME = "SHA256SUMS";
const PLATFORMS = ["linux/amd64", "linux/arm64"];
const PORTABLE_ASSETS = [
  "compose.yaml",
  "compose.deployment-platform-owned.yaml",
  "compose.deployment-managed-tke.yaml",
  "compose.deployment-customer-owned.yaml",
  "compose.fabric-local-docker.yaml",
  "compose.fabric-tencent-tke.yaml",
  "compose.local-workspace.yaml",
  "opl-cloud.env.example"
];
const RECEIPT_KEYS = ["schemaVersion", "kind", "product", "cloudImage", "assets", "provenance"];
const PRODUCT_KEYS = ["repository", "sha", "tree"];
const CLOUD_IMAGE_KEYS = ["repository", "indexRef", "indexDigest", "revision", "platforms"];
const PLATFORM_KEYS = ["platform", "digest"];
const ASSET_KEYS = ["name", "sha256"];
const PROVENANCE_KEYS = ["workflowRepository", "workflowSha", "workflowRunId", "workflowRunAttempt"];
const FORBIDDEN_KEYS = /(?:accountid|operationid|providerid|resourceid|workspaceid|workspaceimage|providerprofile|domain|instance|releasetag|userid|email|password|secret|cookie|csrf|token|kubeconfig|j[245]|buildsource|reconcile|registrymutation)/i;

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function exactKeys(value: unknown, keys: string[]) {
  return isRecord(value) && JSON.stringify(Object.keys(value).sort()) === JSON.stringify([...keys].sort());
}

function hasForbiddenKey(value: unknown): boolean {
  if (Array.isArray(value)) return value.some(hasForbiddenKey);
  if (!isRecord(value)) return false;
  return Object.entries(value).some(([key, child]) => FORBIDDEN_KEYS.test(key) || hasForbiddenKey(child));
}

function sha256(value: string | Buffer) {
  return createHash("sha256").update(value).digest("hex");
}

export function canonicalJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (isRecord(value)) {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJson(value[key])}`).join(",")}}`;
  }
  const encoded = JSON.stringify(value);
  if (encoded === undefined) throw new Error("cloud_candidate_receipt_invalid");
  return encoded;
}

export function validateCloudCandidateReceipt(value: unknown): Record<string, unknown> {
  if (hasForbiddenKey(value)) throw new Error("cloud_candidate_receipt_sensitive");
  if (!exactKeys(value, RECEIPT_KEYS) || value.schemaVersion !== 1 || value.kind !== "opl_cloud_candidate") {
    throw new Error("cloud_candidate_receipt_invalid");
  }

  const product = value.product;
  const cloudImage = value.cloudImage;
  const assets = value.assets;
  const provenance = value.provenance;
  if (!exactKeys(product, PRODUCT_KEYS) || product.repository !== PRODUCT_REPOSITORY ||
      !SHA.test(String(product.sha || "")) || !SHA.test(String(product.tree || "")) ||
      !exactKeys(cloudImage, CLOUD_IMAGE_KEYS) || cloudImage.repository !== CLOUD_IMAGE_REPOSITORY ||
      !DIGEST.test(String(cloudImage.indexDigest || "")) ||
      cloudImage.indexRef !== `${CLOUD_IMAGE_REPOSITORY}@${cloudImage.indexDigest}` ||
      cloudImage.revision !== product.sha || !Array.isArray(cloudImage.platforms) ||
      cloudImage.platforms.length !== PLATFORMS.length ||
      !cloudImage.platforms.every((entry, index) => exactKeys(entry, PLATFORM_KEYS) &&
        entry.platform === PLATFORMS[index] && DIGEST.test(String(entry.digest || ""))) ||
      new Set(cloudImage.platforms.map((entry) => String(entry.digest))).size !== PLATFORMS.length ||
      !Array.isArray(assets) || assets.length !== PORTABLE_ASSETS.length ||
      !assets.every((asset, index) => exactKeys(asset, ASSET_KEYS) &&
        asset.name === PORTABLE_ASSETS[index] && SHA256.test(String(asset.sha256 || ""))) ||
      !exactKeys(provenance, PROVENANCE_KEYS) || provenance.workflowRepository !== PRODUCT_REPOSITORY ||
      !SHA.test(String(provenance.workflowSha || "")) || !POSITIVE_DECIMAL.test(String(provenance.workflowRunId || "")) ||
      !POSITIVE_DECIMAL.test(String(provenance.workflowRunAttempt || ""))) {
    throw new Error("cloud_candidate_receipt_invalid");
  }
  return value;
}

export function candidateReceiptDigest(value: unknown): string {
  const receipt = validateCloudCandidateReceipt(value);
  return `sha256:${sha256(canonicalJson(receipt))}`;
}

export function decodeCandidateReceipt(encoded: string): Record<string, unknown> {
  if (typeof encoded !== "string" || encoded.length === 0 ||
      !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(encoded)) {
    throw new Error("cloud_candidate_receipt_encoding_invalid");
  }
  const decoded = Buffer.from(encoded, "base64");
  if (decoded.toString("base64") !== encoded) throw new Error("cloud_candidate_receipt_encoding_invalid");
  try {
    return validateCloudCandidateReceipt(JSON.parse(decoded.toString("utf8")));
  } catch (error) {
    if (error instanceof Error && error.message.startsWith("cloud_candidate_receipt_")) throw error;
    throw new Error("cloud_candidate_receipt_encoding_invalid");
  }
}

export async function writeCandidateReceipt(path: string, value: unknown) {
  if (!path) throw new Error("cloud_candidate_receipt_output_invalid");
  const receipt = validateCloudCandidateReceipt(value);
  await writeFile(path, `${canonicalJson(receipt)}\n`, { mode: 0o600 });
  await chmod(path, 0o600);
}

async function readReceipt(path: string) {
  try {
    return validateCloudCandidateReceipt(JSON.parse(await readFile(path, "utf8")));
  } catch (error) {
    if (error instanceof Error && error.message.startsWith("cloud_candidate_receipt_")) throw error;
    throw new Error("cloud_candidate_receipt_invalid");
  }
}

export async function validateCandidateBundle(directory: string): Promise<Record<string, unknown>> {
  try {
    const expectedNames = [...PORTABLE_ASSETS, MANIFEST_NAME, CHECKSUM_MANIFEST_NAME];
    const entries = await readdir(directory, { withFileTypes: true });
    if (entries.some((entry) => !entry.isFile()) ||
        JSON.stringify(entries.map((entry) => entry.name).sort()) !== JSON.stringify([...expectedNames].sort())) {
      throw new Error("cloud_candidate_bundle_invalid");
    }

    for (const name of expectedNames) {
      if (!(await lstat(join(directory, name))).isFile()) throw new Error("cloud_candidate_bundle_invalid");
    }

    const receipt = await readReceipt(join(directory, MANIFEST_NAME));
    const assets = receipt.assets as Array<{ name: string; sha256: string }>;
    const checksumSource = await readFile(join(directory, CHECKSUM_MANIFEST_NAME), "utf8");
    if (!checksumSource.endsWith("\n")) throw new Error("cloud_candidate_bundle_invalid");
    const lines = checksumSource.slice(0, -1).split("\n");
    const coveredNames = [...PORTABLE_ASSETS, MANIFEST_NAME];
    if (lines.length !== coveredNames.length) throw new Error("cloud_candidate_bundle_invalid");

    const checksums = lines.map((line, index) => {
      const match = line.match(/^([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]*)$/);
      if (!match || match[2] !== coveredNames[index]) throw new Error("cloud_candidate_bundle_invalid");
      return { sha256: match[1], name: match[2] };
    });

    for (let index = 0; index < PORTABLE_ASSETS.length; index += 1) {
      const digest = sha256(await readFile(join(directory, PORTABLE_ASSETS[index])));
      if (assets[index].sha256 !== digest || checksums[index].sha256 !== digest) {
        throw new Error("cloud_candidate_bundle_invalid");
      }
    }
    const manifestDigest = sha256(await readFile(join(directory, MANIFEST_NAME)));
    if (checksums.at(-1)?.sha256 !== manifestDigest) throw new Error("cloud_candidate_bundle_invalid");
    return receipt;
  } catch {
    throw new Error("cloud_candidate_bundle_invalid");
  }
}

function option(args: string[], name: string) {
  const indexes = args.flatMap((value, index) => value === name ? [index] : []);
  if (indexes.length !== 1 || !args[indexes[0] + 1] || args[indexes[0] + 1].startsWith("--")) {
    throw new Error(`${name.slice(2).replaceAll("-", "_")}_invalid`);
  }
  return args[indexes[0] + 1];
}

export async function main(args = process.argv.slice(2)) {
  const [command] = args;
  if (command === "validate-bundle") return validateCandidateBundle(option(args, "--bundle"));

  const receipt = await readReceipt(option(args, "--receipt"));
  if (command === "validate") return receipt;
  if (command === "digest") {
    process.stdout.write(`${candidateReceiptDigest(receipt)}\n`);
    return receipt;
  }
  if (command === "export-env") {
    const githubEnv = option(args, "--github-env");
    const product = receipt.product as Record<string, unknown>;
    const cloudImage = receipt.cloudImage as Record<string, unknown>;
    const platforms = cloudImage.platforms as Array<Record<string, unknown>>;
    await writeFile(githubEnv, [
      `OPL_CANDIDATE_RECEIPT_DIGEST=${candidateReceiptDigest(receipt)}`,
      `OPL_PRODUCT_SHA=${product.sha}`,
      `OPL_PRODUCT_TREE=${product.tree}`,
      `OPL_CLOUD_IMAGE=${cloudImage.indexRef}`,
      `OPL_CLOUD_IMAGE_DIGEST=${cloudImage.indexDigest}`,
      `OPL_CLOUD_IMAGE_AMD64_DIGEST=${platforms[0].digest}`,
      `OPL_CLOUD_IMAGE_ARM64_DIGEST=${platforms[1].digest}`,
      ""
    ].join("\n"), { flag: "a" });
    return receipt;
  }
  throw new Error("cloud_candidate_receipt_command_invalid");
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : "cloud_candidate_receipt_invalid");
    process.exit(1);
  });
}
