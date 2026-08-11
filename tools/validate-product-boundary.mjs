import { readdir, readFile } from "node:fs/promises";

const root = new URL("../", import.meta.url);
const workflowRoot = new URL(".github/workflows/", root);
const allowedWorkflows = new Set([
  "pull-request-ci.yml",
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

const compose = await readFile(new URL("compose.yaml", root), "utf8");
for (const required of ["control-plane:", "fabric:", "ledger:", "postgres:", "OPL_CLOUD_IMAGE"]) {
  if (!compose.includes(required)) throw new Error(`portable Compose is missing: ${required}`);
}
for (const instanceLeak of ["medopl.cn", "tencentyun.com", "TENCENT_DEPLOY_"]) {
  if (compose.includes(instanceLeak)) throw new Error(`portable Compose contains instance state: ${instanceLeak}`);
}

const contract = JSON.parse(await readFile(new URL("packages/contracts/opl-cloud-distribution-contract.json", root), "utf8"));
if (contract.productRepository !== "gaofeng21cn/one-person-lab-cloud" ||
    contract.instanceHandoff?.repository !== "gaofeng21cn/opl-instance-medopl") {
  throw new Error("distribution owner boundary is invalid");
}

console.log("OPL Cloud product distribution boundary is valid");
