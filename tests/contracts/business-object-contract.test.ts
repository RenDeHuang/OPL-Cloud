import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const businessObjectContractPath = new URL("../../packages/contracts/opl-cloud-business-object-contract.json", import.meta.url);
const businessObjectBacklogPath = new URL("../../packages/contracts/opl-cloud-business-object-backlog.json", import.meta.url);
const routeContractPath = new URL("../../packages/contracts/opl-cloud-route-api-contract.json", import.meta.url);

async function readJson(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

function allRoutes(contract) {
  return [
    ...(contract.publicRoutes || []),
    ...(contract.authRoutes || []),
    ...(contract.consoleRoutes || []),
    ...(contract.adminRoutes || []),
    ...(contract.errorRoutes || [])
  ];
}

test("business object contract defines the current commercial object boundary", async () => {
  const contract = await readJson(businessObjectContractPath);

  assert.equal(contract.schemaVersion, 1);
  assert.equal(contract.owner, "OPL Console");
  assert.equal(contract.purpose, "Machine-readable requirements for current commercial Console objects.");
  assert.deepEqual(contract.repositoryBoundaries, ["opl-console", "opl-fabric", "opl-ledger"]);
  assert.deepEqual(contract.routeKinds, ["read_model", "business_object", "external_integration"]);
  assert.ok(contract.principles.includes("Active business object contract contains only current commercial objects."));
  assert.ok(contract.repoBoundaryRules.includes("Console owns UI, auth, route contracts, and read-model orchestration."));
  assert.ok(contract.repoBoundaryRules.includes("Fabric owns runtime, storage, connector, and agent resource execution boundaries."));
  assert.ok(contract.repoBoundaryRules.includes("Ledger owns evidence, audit, reconciliation, and review policy boundaries."));
});

test("business object contract contains only current OPL Cloud business facts", async () => {
  const contract = await readJson(businessObjectContractPath);
  const kinds = new Map(contract.objectKinds.map((object) => [object.kind, object]));

  for (const [kind, requiredCapabilities] of [
    ["Account", ["list", "detail", "read", "audit"]],
    ["User", ["list", "detail", "read", "write", "action", "audit"]],
    ["Session", ["detail", "read", "action", "audit"]],
    ["ComputeAllocation", ["list", "detail", "read", "write", "action", "evidence"]],
    ["StorageVolume", ["list", "detail", "read", "write", "action", "evidence"]],
    ["StorageAttachment", ["list", "detail", "read", "write", "action", "evidence"]],
    ["Workspace", ["list", "detail", "read", "write", "action"]],
    ["Wallet", ["list", "detail", "read", "action", "audit"]],
    ["LedgerEntry", ["list", "read", "evidence", "audit"]],
    ["FabricOperation", ["list", "detail", "read", "evidence", "audit"]],
    ["AdminAuditEvent", ["list", "read", "audit"]],
    ["SupportTicketMapping", ["list", "detail", "read", "write", "audit"]]
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
    "ComputeAllocation",
    "FabricOperation",
    "LedgerEntry",
    "Session",
    "StorageAttachment",
    "StorageVolume",
    "SupportTicketMapping",
    "User",
    "Wallet",
    "Workspace"
  ].sort());
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
    "url",
    "access.account",
    "access.password",
    "access.tokenStatus",
    "access.credentialStatus",
    "runtime.status",
    "state"
  ]);
  assert.ok(contract.principles.includes("Destroying compute suspends the Workspace URL and retains storage; rebuilding compute reuses the same Workspace URL and StorageVolume."));
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
    "billingStatus",
    "destroyedAt",
    "lastProviderSyncAt"
  ]);
  assert.ok(computeAllocation.requiredProviderFields.includes("nodeName"), "nodeName must be required for a running allocation");
});

test("route object kinds map to committed object specs and owner repos", async () => {
  const businessContract = await readJson(businessObjectContractPath);
  const routeContract = await readJson(routeContractPath);
  const objectSpecs = new Map(businessContract.objectKinds.map((object) => [object.kind, object]));

  for (const route of allRoutes(routeContract).filter((entry) => entry.objectKind)) {
    const objectSpec = objectSpecs.get(route.objectKind);
    assert.ok(objectSpec, `missing object spec for ${route.objectKind} on ${route.path}`);
    assert.equal(objectSpec.ownerRepo, route.ownerRepo, `${route.path} ownerRepo must match ${route.objectKind}`);
    assert.ok((objectSpec.routeKinds || [objectSpec.routeKind]).includes(route.routeKind), `${route.path} routeKind must match ${route.objectKind}`);
  }
});

test("implemented commercial objects satisfy capability requirements across their route cluster", async () => {
  const businessContract = await readJson(businessObjectContractPath);
  const routeContract = await readJson(routeContractPath);
  const objectSpecs = new Map(businessContract.objectKinds.map((object) => [object.kind, object]));
  const dynamicRoutes = allRoutes(routeContract).filter((route) => (
    route.status === "implemented"
    && businessContract.routeKinds.includes(route.routeKind)
    && route.objectKind
  ));

  assert.ok(dynamicRoutes.length > 0, "there should be implemented dynamic routes");
  const implementedObjectKinds = new Set(dynamicRoutes.map((route) => route.objectKind));
  for (const objectKind of implementedObjectKinds) {
    const spec = objectSpecs.get(objectKind);
    assert.ok(spec, `${objectKind} must map to an object spec`);
    const routes = allRoutes(routeContract).filter((route) => route.objectKind === objectKind && route.contractLifecycle !== "dynamic_prune");
    const capabilities = new Set(routes.flatMap((route) => route.capabilities || []));
    for (const capability of spec.requiredCapabilitiesForImplemented) {
      assert.ok(capabilities.has(capability), `${objectKind} missing ${capability} capability`);
    }
    if (spec.evidenceRequired) {
      assert.ok(capabilities.has("audit") || capabilities.has("evidence"), `${objectKind} must include audit/evidence`);
    }
  }
});

test("active business contract excludes future and prune object shells", async () => {
  const businessContract = await readJson(businessObjectContractPath);
  const backlog = await readJson(businessObjectBacklogPath);
  const activeKinds = new Set(businessContract.objectKinds.map((object) => object.kind));
  const backlogKinds = new Set((backlog.objectKinds || []).map((object) => object.kind));

  for (const object of businessContract.objectKinds) {
    assert.equal(object.lifecycle, "current", `${object.kind} must be a current commercial object`);
  }

  for (const kind of [
    "ApprovalQueue",
    "ConnectorApproval",
    "EnvironmentTemplateApproval",
    "AgentPackageApproval",
    "LedgerReviewPolicy",
    "RuntimeProviderDiagnostics"
  ]) {
    assert.equal(activeKinds.has(kind), false, `${kind} must not be active commercial object truth`);
    assert.equal(backlogKinds.has(kind), true, `${kind} must move to business object backlog`);
  }
  for (const kind of ["ComputePool", "RuntimeTemplate", "Usage", "ResourceRelationship", "ProductionVerification", "GatewayIntegration", "WorkspaceAccess", "RuntimeReadiness"]) {
    assert.equal(activeKinds.has(kind), false, `${kind} must not be an active business object`);
  }
});
