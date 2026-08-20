import { readdir, readFile } from "node:fs/promises";
import YAML from "yaml";

const root = new URL("../", import.meta.url);
const workflowRoot = new URL(".github/workflows/", root);
const allowedWorkflows = new Set([
  "build-opl-cloud-candidate.yml",
  "clean-host-qualification.yml",
  "codeql.yml",
  "pull-request-ci.yml",
  "qualification.yml",
  "release-opl-cloud-image.yml",
  "whitepaper.yml",
]);

const workflows = (await readdir(workflowRoot)).filter((name) => name.endsWith(".yml"));
const unexpected = workflows.filter((name) => !allowedWorkflows.has(name));
if (unexpected.length > 0) {
  throw new Error(`instance deployment workflows remain in Cloud: ${unexpected.join(",")}`);
}

const release = await readFile(new URL(".github/workflows/release-opl-cloud-image.yml", root), "utf8");
for (const forbidden of ["environment: production", "tencentyun.com", "workflow_dispatch:\n    inputs:\n      cloud_image:"]) {
  if (release.includes(forbidden)) throw new Error(`Cloud release owns an instance concern: ${forbidden}`);
}
for (const required of ["ghcr.io/${{ github.repository }}", "linux/amd64,linux/arm64", "gh release create", "compose.yaml"]) {
  if (!release.includes(required)) throw new Error(`Cloud release is missing: ${required}`);
}

const candidate = await readFile(new URL(".github/workflows/build-opl-cloud-candidate.yml", root), "utf8");
const candidateWorkflow = YAML.parse(candidate);
const candidateInputs = Object.keys(candidateWorkflow.on?.workflow_dispatch?.inputs || {});
const candidateJobs = Object.keys(candidateWorkflow.jobs || {});
const candidateJob = candidateWorkflow.jobs?.candidate;
if (JSON.stringify(candidateInputs) !== JSON.stringify(["product_sha"]) ||
    JSON.stringify(candidateJobs) !== JSON.stringify(["candidate"]) ||
    candidateJob?.environment !== undefined ||
    candidateJob?.env?.IMAGE_REPOSITORY !== "ghcr.io/${{ github.repository }}" ||
    candidateJob?.permissions?.contents !== "read" || candidateJob?.permissions?.packages !== "write") {
  throw new Error("Cloud Candidate workflow authority boundary is invalid");
}
const candidateCommands = (candidateJob.steps || []).map((step) => step.run || "").join("\n");
for (const forbidden of ["tencentyun.com", "medopl.cn", "gh release", "git tag", "kubectl", "WORKSPACE_IMAGE", "workspace_image", "releaseTag"]) {
  if (candidateCommands.includes(forbidden) || candidateInputs.includes(forbidden)) {
    throw new Error(`Cloud Candidate owns a forbidden concern: ${forbidden}`);
  }
}
for (const required of ["--platform linux/amd64,linux/arm64", "--push", "tools/cloud-candidate-receipt.ts validate-bundle", "opl-cloud-candidate.json", "SHA256SUMS"]) {
  if (!candidateCommands.includes(required)) throw new Error(`Cloud Candidate is missing: ${required}`);
}

const compose = await readFile(new URL("compose.yaml", root), "utf8");
for (const required of ["control-plane:", "fabric:", "ledger:", "postgres:", "OPL_CLOUD_IMAGE"]) {
  if (!compose.includes(required)) throw new Error(`portable Compose is missing: ${required}`);
}
for (const instanceLeak of ["medopl.cn", "tencentyun.com", "TENCENT_DEPLOY_"]) {
  if (compose.includes(instanceLeak)) throw new Error(`portable Compose contains instance state: ${instanceLeak}`);
}
const commonImage = compose.match(/^  image: (.+)$/m)?.[1] || "";
for (const service of ["control-plane", "fabric", "ledger", "postgres"]) {
  const block = compose.match(new RegExp(`^  ${service}:\\n([\\s\\S]*?)(?=^  [a-z][a-z0-9-]*:|^volumes:|^configs:|^networks:)`, "m"))?.[1] || "";
  const image = block.match(/^    image: (.+)$/m)?.[1] || commonImage;
  if (!/@sha256:[0-9a-f]{64}$/.test(image) && image !== "${OPL_CLOUD_IMAGE:?Set OPL_CLOUD_IMAGE to an immutable GHCR digest}") {
    throw new Error(`portable Compose image is not digest-pinned: ${service}`);
  }
}

const dockerfile = await readFile(new URL("Dockerfile", root), "utf8");
const baseImages = dockerfile
  .split("\n")
  .map((line) => line.trim())
  .filter((line) => line.startsWith("FROM"));
for (const line of baseImages) {
  if (!/@sha256:[0-9a-f]{64}\b/.test(line)) {
    throw new Error(`release base image is not digest-pinned: ${line}`);
  }
}
const kubectlAmd64 = dockerfile.match(/amd64\)\s*KUBECTL_SHA256="([0-9a-f]{64})"/);
const kubectlArm64 = dockerfile.match(/arm64\)\s*KUBECTL_SHA256="([0-9a-f]{64})"/);
if (!kubectlAmd64 || !kubectlArm64) {
  throw new Error("release kubectl download is not checksum-bound per architecture");
}
if (!/sha256sum\s+-c\s/.test(dockerfile)) {
  throw new Error("release kubectl download is not checksum-verified");
}

const contract = JSON.parse(await readFile(new URL("packages/contracts/opl-cloud-distribution-contract.json", root), "utf8"));
const candidateContract = JSON.parse(await readFile(new URL("packages/contracts/opl-cloud-candidate-receipt-contract.json", root), "utf8"));
const candidateReceipt = candidateContract.candidateReceiptV2;
const expectedCandidateAssets = [
  "compose.yaml",
  "compose.deployment-platform-owned.yaml",
  "compose.deployment-managed-tke.yaml",
  "compose.deployment-customer-owned.yaml",
  "compose.fabric-local-docker.yaml",
  "compose.fabric-tencent-tke.yaml",
  "compose.local-workspace.yaml",
  "opl-cloud.env.example"
];
if (candidateContract.schemaVersion !== 2 || candidateReceipt?.schemaVersion !== 2 ||
    JSON.stringify(candidateReceipt?.platforms) !== JSON.stringify(["linux/amd64", "linux/arm64"]) ||
    JSON.stringify(candidateReceipt?.portableAssets) !== JSON.stringify(expectedCandidateAssets) ||
    candidateReceipt?.manifest !== "opl-cloud-candidate.json" ||
    candidateReceipt?.checksumManifest !== "SHA256SUMS") {
  throw new Error("Candidate receipt contract boundary is invalid");
}
if (contract.schemaVersion !== 2 || contract.productRepository !== "gaofeng21cn/one-person-lab-cloud" ||
    contract.instanceHandoff?.repository !== "gaofeng21cn/opl-instance-medopl" ||
    contract.candidate?.contract !== "packages/contracts/opl-cloud-candidate-receipt-contract.json#candidateReceiptV2" ||
    JSON.stringify(Object.keys(contract.candidate || {}).sort()) !== JSON.stringify([
      "artifactLocator", "contract", "formalPublication", "instanceDeployment", "registry", "workflow"
    ]) ||
    contract.candidate?.artifactLocator?.kind !== "github_actions_artifact" ||
    JSON.stringify(contract.candidate?.artifactLocator?.exactKeys) !== JSON.stringify(["workflowRunId", "artifactName"]) ||
    contract.candidate?.artifactLocator?.workflowRunIdField !== "provenance.workflowRunId" ||
    contract.candidate?.artifactLocator?.artifactNameTemplate !== "opl-cloud-candidate-{product.sha}" ||
    contract.candidate?.artifactLocator?.artifactNameProductShaField !== "product.sha" ||
    contract.instanceHandoff?.artifactLocatorContract !== "packages/contracts/opl-cloud-distribution-contract.json#candidate.artifactLocator" ||
    JSON.stringify(contract.instanceHandoff?.inputs) !== JSON.stringify(["candidate_manifest_b64"])) {
  throw new Error("distribution owner boundary is invalid");
}

const productContractSource = await readFile(new URL("packages/contracts/opl-cloud-product-contract.json", root), "utf8");
const productContract = JSON.parse(productContractSource);
if (productContractSource.includes("medopl.cn") ||
    productContract.access?.originOwner !== "instance_or_installer_profile" ||
    productContract.access?.urlPathPattern !== "/w/<workspaceId>/") {
  throw new Error("product contract contains an instance-owned Workspace origin");
}

console.log("OPL Cloud product distribution boundary is valid");
