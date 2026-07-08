import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const productContractPath = new URL("../../packages/contracts/opl-cloud-product-contract.json", import.meta.url);
const businessObjectContractPath = new URL("../../packages/contracts/opl-cloud-business-object-contract.json", import.meta.url);

async function readJson(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

test("product contract treats app image as runtime template, not commercial resource", async () => {
  const product = await readJson(productContractPath);

  assert.equal(product.resourceMapping.workspaceUrlEntry, 1);
  assert.equal(product.resourceMapping.computeAllocation, 1);
  assert.equal(product.resourceMapping.storageVolume, 1);
  assert.equal(product.resourceMapping.storageAttachment, 1);
  assert.equal(product.resourceMapping.runtimeTemplate, 1);
  assert.equal(product.resourceMapping.onePersonLabAppDocker, undefined);
  assert.equal(product.runtimeTemplatePolicy.defaultTemplateId, "one-person-lab-app");
  assert.equal(product.runtimeTemplatePolicy.billingObject, false);
  assert.match(product.runtimeTemplatePolicy.ownershipRule, /never own accounts/);
});

test("business object contract keeps runtime template out of billing ownership", async () => {
  const contract = await readJson(businessObjectContractPath);
  const runtimeTemplate = contract.objectKinds.find((object) => object.kind === "RuntimeTemplate");

  assert.equal(runtimeTemplate, undefined, "RuntimeTemplate must not be an active business object");
  assert.ok(contract.principles.includes("RuntimeTemplate/ImageRef is deployable runtime configuration only; it is not a billing object, storage owner, or Workspace identity."));
});
