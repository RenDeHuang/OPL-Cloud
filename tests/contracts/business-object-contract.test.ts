import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const businessObjectContractPath = new URL("../../packages/contracts/opl-cloud-business-object-contract.json", import.meta.url);

async function readJson(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

test("business object contract defines the current commercial object boundary", async () => {
  const contract = await readJson(businessObjectContractPath);

	assert.equal(contract.schemaVersion, 5);
  assert.equal(contract.owner, "OPL Console");
  assert.equal(contract.purpose, "Machine-readable requirements for current commercial Console objects.");
  assert.deepEqual(contract.repositoryBoundaries, ["opl-console", "opl-fabric", "opl-ledger"]);
  assert.deepEqual(contract.routeKinds, ["read_model", "business_object", "external_integration"]);
  assert.ok(contract.principles.includes("Active business object contract contains only current commercial objects."));
  assert.ok(contract.repoBoundaryRules.includes("Console owns UI and product presentation through Control Plane APIs only; Sub2API authenticates customer credentials."));
  assert.ok(contract.repoBoundaryRules.includes("Fabric owns compute, storage, attachment, Workspace runtime, and provider execution boundaries."));
  assert.ok(contract.repoBoundaryRules.includes("Ledger owns evidence, audit, reconciliation, and review policy boundaries."));
});

test("business object contract contains only current OPL Cloud business facts", async () => {
  const contract = await readJson(businessObjectContractPath);
  const kinds = new Map(contract.objectKinds.map((object) => [object.kind, object]));

  for (const [kind, requiredCapabilities] of [
    ["Account", ["list", "detail", "read", "audit"]],
    ["User", ["list", "detail", "read", "write", "action", "audit"]],
    ["Session", ["detail", "read", "action", "audit"]],
    ["ComputeAllocation", ["list", "detail", "read", "evidence"]],
    ["StorageVolume", ["list", "detail", "read", "evidence"]],
    ["StorageAttachment", ["list", "detail", "read", "evidence"]],
    ["Workspace", ["list", "detail", "read", "write", "action"]],
    ["Balance", ["list", "detail", "read", "audit"]],
    ["EvidenceReceipt", ["list", "read", "evidence", "audit"]],
    ["FabricOperation", ["list", "detail", "read", "evidence", "audit"]],
    ["AdminAuditEvent", ["list", "read", "audit"]],
    ["SupportTicketMapping", ["list", "detail", "read", "write", "audit"]],
    ["Announcement", ["list", "detail", "read", "write", "audit"]]
  ]) {
    const object = kinds.get(kind);
    assert.ok(object, `missing object kind ${kind}`);
    for (const capability of requiredCapabilities) {
      assert.ok(object.requiredCapabilitiesForImplemented.includes(capability), `${kind} must require ${capability}`);
    }
  }

  assert.deepEqual([...kinds.keys()].sort(), [
    "Account",
    "AdminAuditEvent",
    "Announcement",
    "ComputeAllocation",
    "Balance",
    "EvidenceReceipt",
    "FabricOperation",
    "Session",
    "StorageAttachment",
    "StorageVolume",
    "SupportTicketMapping",
    "User",
    "Workspace"
  ].sort());
  for (const kind of ["ComputeAllocation", "StorageVolume", "StorageAttachment"]) {
    assert.equal(kinds.get(kind).customerSurface, "workspace_detail_read_only");
    assert.equal(kinds.get(kind).requiredCapabilitiesForImplemented.includes("write"), false);
    assert.equal(kinds.get(kind).requiredCapabilitiesForImplemented.includes("action"), false);
  }
});

test("Workspace contract is the stable URL, storage, and current runtime pointer", async () => {
  const contract = await readJson(businessObjectContractPath);
  const workspace = contract.objectKinds.find((object) => object.kind === "Workspace");

  assert.ok(workspace, "Workspace must be current object truth");
  assert.match(workspace.boundary || "", /Stable URL subject/);
  assert.deepEqual(workspace.requiredFields, [
    "storageId",
    "currentComputeAllocationId",
    "currentAttachmentId",
    "workspaceApiKeyId",
    "url",
    "access.account",
    "access.credentialStatus",
    "access.credentialVersion",
    "runtime.status",
    "state"
  ]);
  assert.ok(contract.principles.includes("Ordinary Runtime status is non-secret; only the Workspace owner user may reveal or rotate the Runtime password through private, no-store commands, and passwords never enter the persisted Workspace projection, operation payloads, audit, logs, or Ledger."));
  assert.ok(contract.principles.includes("Destroying compute suspends the Workspace URL and retains storage; rebuilding compute with the same Workspace URL and StorageVolume is a future target with no Pilot customer route."));
  assert.ok(contract.principles.includes("Destroying storage makes the Workspace unrecoverable because the file body is gone."));
});

test("ComputeAllocation contract requires dedicated CVM or Kubernetes node identity", async () => {
  const contract = await readJson(businessObjectContractPath);
  const computeAllocation = contract.objectKinds.find((object) => object.kind === "ComputeAllocation");

  assert.ok(computeAllocation, "ComputeAllocation must be current object truth");
  assert.match(computeAllocation.boundary || "", /dedicated CVM/i);
  assert.deepEqual(computeAllocation.requiredProviderFields, [
    "nodePoolId",
    "cvmInstanceId",
    "machineName",
    "nodeName",
    "privateIp",
    "publicIp",
    "ownerAccountId",
    "workspaceIds",
    "providerStatus",
    "destroyedAt",
    "lastProviderSyncAt"
  ]);
  assert.ok(computeAllocation.requiredProviderFields.includes("nodeName"), "nodeName must be required for a running allocation");
});
