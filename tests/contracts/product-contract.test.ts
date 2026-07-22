import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const productContractPath = new URL("../../packages/contracts/opl-cloud-product-contract.json", import.meta.url);
const businessObjectContractPath = new URL("../../packages/contracts/opl-cloud-business-object-contract.json", import.meta.url);
const fabricCatalogContractPath = new URL("../../packages/contracts/opl-cloud-fabric-resource-catalog-contract.json", import.meta.url);

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
  assert.equal(product.resourceMapping.urlToken, undefined);
  assert.equal(product.resourceMapping.onePersonLabAppDocker, undefined);
  assert.equal(product.runtimeTemplatePolicy.defaultTemplateId, "one-person-lab-app");
  assert.equal(product.runtimeTemplatePolicy.billingObject, false);
  assert.match(product.runtimeTemplatePolicy.ownershipRule, /never own accounts/);
  assert.deepEqual(product.defaultPackages.map((plan) => plan.id), ["basic", "pro"]);
  assert.deepEqual(product.defaultPackages.map(({ id, cpu, memoryGb, diskGb, targetPackage }) => ({ id, cpu, memoryGb, diskGb, targetPackage })), [
    { id: "basic", cpu: 2, memoryGb: 4, diskGb: 10, targetPackage: true },
    { id: "pro", cpu: 8, memoryGb: 16, diskGb: 100, targetPackage: true }
  ]);
  assert.equal(product.defaultPackages.some((plan) => "available" in plan), false);
  assert.equal(product.access.urlPattern, "https://workspace.medopl.cn/w/<workspaceId>/");
  assert.equal(product.access.mode, "runtime_password");
  assert.equal(product.access.requiresLogin, true);
  assert.equal(product.access.tokenLifetime, undefined);
  assert.equal(product.access.rotationPolicy, undefined);
});

test("business object contract keeps runtime template out of billing ownership", async () => {
  const contract = await readJson(businessObjectContractPath);
  const runtimeTemplate = contract.objectKinds.find((object) => object.kind === "RuntimeTemplate");

  assert.equal(runtimeTemplate, undefined, "RuntimeTemplate must not be an active business object");
  assert.ok(contract.principles.includes("RuntimeTemplate/ImageRef is deployable runtime configuration only; it is not a billing object, storage owner, or Workspace identity."));
});

test("Fabric catalog is the live availability authority for both target packages", async () => {
  const catalog = await readJson(fabricCatalogContractPath);

  assert.deepEqual(catalog.supportedPackages, ["basic", "pro"]);
  assert.deepEqual(catalog.productionAvailability, {
    basic: true,
    pro: true,
    authority: "GET /fabric/catalog workspacePackages[].available"
  });
  assert.equal(catalog.availabilityAuthority, "GET /fabric/catalog workspacePackages[].available");
  assert.equal(catalog.availabilityMeaning, "product_open_not_tencent_capacity");
  assert.equal(catalog.capacityAuthority, "Tencent MonthlyPreflight immediately before first debit");
  assert.equal("currentAvailablePackages" in catalog, false);
});
