import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const contract = async (name: string) => JSON.parse(await readFile(new URL(`packages/contracts/${name}`, root), "utf8"));

test("CONTRACT-OWNER-02 has one current owner for each retained hard boundary", async () => {
  const [billing, controlPlane, fabric, ledger, legacy] = await Promise.all([
    contract("opl-cloud-billing-ledger-contract.json"),
    contract("opl-cloud-control-plane-launch-contract.json"),
    contract("opl-cloud-fabric-launch-binding-contract.json"),
    contract("opl-cloud-evidence-ledger-contract.json"),
    contract("opl-cloud-launch-freeze-contract.json")
  ]);

  assert.equal(billing.owner, "OPL Control Plane");
  assert.ok(billing.chargePolicy);
  assert.equal(
    billing.workspaceLaunchFulfillment.manualReviewRecoveryOwnerContract,
    "opl-cloud-control-plane-launch-contract.json#recovery"
  );

  assert.equal(controlPlane.owner, "services/control-plane");
  assert.ok(controlPlane.launchOperation);
  assert.ok(controlPlane.recovery);
  assert.equal(controlPlane.providerResources, undefined);
  assert.equal(controlPlane.receipts, undefined);

  assert.equal(fabric.owner, "services/fabric");
  assert.ok(fabric.launchBinding);
  assert.ok(fabric.stageOperations);
  assert.equal(fabric.balance, undefined);
  assert.equal(fabric.receipts, undefined);

  assert.equal(ledger.owner, "OPL Ledger");
  assert.ok(ledger.workspaceMonthlyBillingReceiptV1);
  assert.ok(ledger.generalReceiptV1);

  assert.equal(legacy.state, "migration");
  assert.equal(legacy.monthlySettlement, undefined);
  assert.deepEqual(legacy.canonicalOwners, {
    settlement: "opl-cloud-billing-ledger-contract.json",
    controlPlaneLaunchRecovery: "opl-cloud-control-plane-launch-contract.json",
    fabricProviderResourceBinding: "opl-cloud-fabric-launch-binding-contract.json",
    ledgerReceiptEvidence: "opl-cloud-evidence-ledger-contract.json"
  });
  assert.deepEqual(Object.keys(legacy).sort(), [
    "canonicalOwners", "lifecycle", "machineBoundary", "owner", "providerProcurement",
    "purpose", "schemaVersion", "state", "workspaceLaunch"
  ]);
});
