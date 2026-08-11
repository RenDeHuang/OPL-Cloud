import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const contract = async (name: string) => JSON.parse(await readFile(new URL(`packages/contracts/${name}`, root), "utf8"));

test("CONTRACT-OWNER-02 has one current owner for each retained hard boundary", async () => {
  const [billing, controlPlane, fabric, ledger, serviceBoundary] = await Promise.all([
    contract("opl-cloud-billing-ledger-contract.json"),
    contract("opl-cloud-control-plane-launch-contract.json"),
    contract("opl-cloud-fabric-launch-binding-contract.json"),
    contract("opl-cloud-evidence-ledger-contract.json"),
    contract("opl-cloud-service-boundary-contract.json")
  ]);

  assert.equal(billing.owner, "OPL Control Plane");
  assert.ok(billing.chargePolicy);
  assert.equal(
    billing.workspaceLaunchFulfillment.manualReviewRecoveryContract,
    "opl-cloud-control-plane-launch-contract.json#recovery"
  );
  assert.equal(billing.workspaceLaunchFulfillment.manualReviewRecoveryOwnerContract, undefined);

  assert.equal(controlPlane.owner, "services/control-plane");
  assert.ok(controlPlane.launchOperation);
  assert.ok(controlPlane.recovery);
  assert.equal(controlPlane.providerResources, undefined);
  assert.equal(controlPlane.receipts, undefined);

  assert.equal(fabric.owner, "services/fabric");
  assert.ok(fabric.launchBinding);
  assert.ok(fabric.workspaceLaunchApi);
  assert.ok(fabric.stageOperations);
  assert.equal(fabric.stageReadbackApi, undefined);
  assert.equal(fabric.balance, undefined);
  assert.equal(fabric.receipts, undefined);

  assert.equal(ledger.owner, "OPL Ledger");
  assert.ok(ledger.workspaceMonthlyBillingReceiptV1);
  assert.ok(ledger.generalReceiptV1);

  assert.deepEqual(serviceBoundary.focusedOwners, {
    settlement: "opl-cloud-billing-ledger-contract.json",
    controlPlaneLaunchRecovery: "opl-cloud-control-plane-launch-contract.json",
    fabricLaunchBinding: "opl-cloud-fabric-launch-binding-contract.json",
    ledgerReceiptEvidence: "opl-cloud-evidence-ledger-contract.json"
  });
  const contractFiles = await readdir(new URL("packages/contracts/", root));
  assert.equal(contractFiles.includes("opl-cloud-launch-freeze-contract.json"), false);
});
