import { createHash } from "node:crypto";
import { chmod, readFile, writeFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

const SHA = /^[0-9a-f]{40}$/;
const DIGEST = /^sha256:[0-9a-f]{64}$/;
const POSITIVE_DECIMAL = /^[1-9][0-9]*$/;
const IMAGE_REF = /^[a-z0-9.-]+(?::[1-9][0-9]*)?\/[a-z0-9._/-]+@sha256:[0-9a-f]{64}$/;
const PRODUCT_REPOSITORY = "gaofeng21cn/one-person-lab-cloud";
const CLOUD_IMAGE_REPOSITORY = "ghcr.io/gaofeng21cn/one-person-lab-cloud";
const RECEIPT_KEYS = ["schemaVersion", "kind", "product", "platform", "cloudImage", "workspaceImage", "provenance"];
const PRODUCT_KEYS = ["repository", "sha", "tree"];
const CLOUD_IMAGE_KEYS = ["repository", "ref", "digest", "revision"];
const WORKSPACE_IMAGE_KEYS = ["ref", "digest"];
const PROVENANCE_KEYS = ["workflowRepository", "workflowSha", "workflowRunId", "workflowRunAttempt"];
const FORBIDDEN_KEYS = /(?:accountid|operationid|providerid|resourceid|workspaceid|userid|email|password|secret|cookie|csrf|token|kubeconfig|j[245]|buildsource|reconcile|registrymutation)/i;

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
  if (!exactKeys(value, RECEIPT_KEYS) || value.schemaVersion !== 1 || value.kind !== "opl_cloud_candidate" || value.platform !== "linux/amd64") {
    throw new Error("cloud_candidate_receipt_invalid");
  }

  const product = value.product;
  const cloudImage = value.cloudImage;
  const workspaceImage = value.workspaceImage;
  const provenance = value.provenance;
  if (!exactKeys(product, PRODUCT_KEYS) || product.repository !== PRODUCT_REPOSITORY ||
      !SHA.test(String(product.sha || "")) || !SHA.test(String(product.tree || "")) ||
      !exactKeys(cloudImage, CLOUD_IMAGE_KEYS) || cloudImage.repository !== CLOUD_IMAGE_REPOSITORY ||
      !DIGEST.test(String(cloudImage.digest || "")) || cloudImage.ref !== `${CLOUD_IMAGE_REPOSITORY}@${cloudImage.digest}` ||
      cloudImage.revision !== product.sha ||
      !exactKeys(workspaceImage, WORKSPACE_IMAGE_KEYS) || !DIGEST.test(String(workspaceImage.digest || "")) ||
      !IMAGE_REF.test(String(workspaceImage.ref || "")) || !String(workspaceImage.ref).endsWith(`@${workspaceImage.digest}`) ||
      !exactKeys(provenance, PROVENANCE_KEYS) || provenance.workflowRepository !== PRODUCT_REPOSITORY ||
      !SHA.test(String(provenance.workflowSha || "")) || !POSITIVE_DECIMAL.test(String(provenance.workflowRunId || "")) ||
      !POSITIVE_DECIMAL.test(String(provenance.workflowRunAttempt || ""))) {
    throw new Error("cloud_candidate_receipt_invalid");
  }
  return value;
}

export function candidateReceiptDigest(value: unknown): string {
  const receipt = validateCloudCandidateReceipt(value);
  return `sha256:${createHash("sha256").update(canonicalJson(receipt)).digest("hex")}`;
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

function option(args: string[], name: string) {
  const indexes = args.flatMap((value, index) => value === name ? [index] : []);
  if (indexes.length !== 1 || !args[indexes[0] + 1] || args[indexes[0] + 1].startsWith("--")) {
    throw new Error(`${name.slice(2).replaceAll("-", "_")}_invalid`);
  }
  return args[indexes[0] + 1];
}

async function readReceipt(path: string) {
  try {
    return validateCloudCandidateReceipt(JSON.parse(await readFile(path, "utf8")));
  } catch (error) {
    if (error instanceof Error && error.message.startsWith("cloud_candidate_receipt_")) throw error;
    throw new Error("cloud_candidate_receipt_invalid");
  }
}

export async function main(args = process.argv.slice(2)) {
  const [command] = args;
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
    const workspaceImage = receipt.workspaceImage as Record<string, unknown>;
    await writeFile(githubEnv, [
      `OPL_CANDIDATE_RECEIPT_DIGEST=${candidateReceiptDigest(receipt)}`,
      `OPL_PRODUCT_SHA=${product.sha}`,
      `OPL_PRODUCT_TREE=${product.tree}`,
      `OPL_CLOUD_IMAGE=${cloudImage.ref}`,
      `OPL_CLOUD_IMAGE_DIGEST=${cloudImage.digest}`,
      `OPL_WORKSPACE_IMAGE=${workspaceImage.ref}`,
      `OPL_WORKSPACE_IMAGE_DIGEST=${workspaceImage.digest}`,
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
