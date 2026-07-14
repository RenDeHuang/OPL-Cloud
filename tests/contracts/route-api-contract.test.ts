import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const contractPath = new URL("../../packages/contracts/opl-cloud-route-api-contract.json", import.meta.url);
const backlogPath = new URL("../../packages/contracts/opl-cloud-route-backlog.json", import.meta.url);

async function readJson(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

function expectedRoutesFromContract(contract) {
  return [
    ...(contract.publicRoutes || []),
    ...(contract.authRoutes || []),
    ...(contract.consoleRoutes || []),
    ...(contract.adminRoutes || []),
    ...(contract.errorRoutes || [])
  ];
}

test("OPL Cloud route/API contract is the current commercial Console truth", async () => {
  const contract = await readJson(contractPath);

  assert.equal(contract.schemaVersion, 1);
  assert.equal(contract.owner, "OPL Console");
  assert.equal(contract.purpose, "Commercial route, permission, page, API client, server route, and service boundary map.");
  assert.deepEqual(contract.repositoryBoundaries, ["opl-console", "opl-fabric", "opl-ledger"]);
  assert.deepEqual(contract.statuses, ["implemented", "folded_into_parent", "external"]);
  assert.deepEqual(contract.routeKinds, ["static_content", "auth_flow", "read_model", "business_object", "external_integration"]);
  assert.deepEqual(contract.contractLifecycles, ["current", "folded_parent"]);
  assert.ok(contract.boundaryRules.includes("Console calls only Control Plane product APIs."));
  assert.ok(contract.boundaryRules.includes("Control Plane orchestrates typed Fabric, Ledger, and Sub2API clients without exposing generic proxy routes."));
  assert.ok(contract.boundaryRules.includes("Active route contract contains only current commercial truth; future, reserved, and prune candidates live in route backlog."));
  assert.ok(contract.boundaryRules.includes("Every enabled route has a stable route id used by menus, actions, and routeTo()."));
});

test("active route contract excludes future, reserved, prune, and retired route shells", async () => {
  const contract = await readJson(contractPath);
  const backlog = await readJson(backlogPath);
  const activeRoutes = expectedRoutesFromContract(contract);
  const activePaths = new Set(activeRoutes.map((route) => route.path));
  const backlogPaths = new Set((backlog.routes || []).map((route) => route.path));

  assert.equal(activeRoutes.some((route) => route.status === "reserved"), false, "active contract must not include reserved routes");
  assert.equal(activeRoutes.some((route) => route.contractLifecycle === "long_term_gap"), false, "active contract must not include long-term gaps");
  assert.equal(activeRoutes.some((route) => route.contractLifecycle === "dynamic_prune"), false, "active contract must not include prune candidates");

  for (const path of [
    "/register",
    "/invite/accept",
    "/email/verify",
    "/forgot-password",
    "/reset-password",
    "/console/resources/connectors",
    "/console/approvals",
    "/admin/fabric/connectors",
    "/admin/ledger/policies",
    "/admin/runtime/kubernetes"
  ]) {
    assert.equal(activePaths.has(path), false, `${path} must be removed from active contract`);
    assert.equal(backlogPaths.has(path), true, `${path} must be tracked in route backlog`);
  }

  assert.deepEqual([...new Set(activeRoutes.flatMap((route) => route.apiRoutes || []))].sort(), [
    "GET /api/auth/me",
    "GET /api/billing/receipts/:id",
    "GET /api/compute-allocations",
    "GET /api/compute-allocations/:id",
    "GET /api/compute-pools",
    "GET /api/management/state",
    "GET /api/operator/archive",
    "GET /api/operator/summary",
    "GET /api/pricing/catalog",
    "GET /api/production/readiness",
    "GET /api/runtime/readiness",
    "GET /api/state",
    "GET /api/support/tickets",
    "POST /api/auth/login",
    "POST /api/auth/logout",
    "POST /api/auth/operator-login",
    "POST /api/billing/reconciliation",
    "POST /api/compute-allocations",
    "POST /api/compute-allocations/:id/destroy",
    "POST /api/operator/archive-terminal-resources",
    "POST /api/operator/cleanup-workspace-access",
    "POST /api/organizations",
    "POST /api/organizations/members",
    "POST /api/pricing/preview",
    "POST /api/resources/:id/auto-renew",
    "POST /api/resources/:id/renew",
    "POST /api/storage-attachments",
    "POST /api/storage-attachments/detach",
    "POST /api/storage-volumes",
    "POST /api/storage-volumes/destroy",
    "POST /api/support/tickets",
    "POST /api/users",
    "POST /api/users/delete",
    "POST /api/users/disable",
    "POST /api/workspaces",
    "POST /api/workspaces/delete-token",
    "POST /api/workspaces/reset-token",
    "POST /api/workspaces/runtime-status"
  ].sort());
});

test("active route contract models compute pools before account compute allocations", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract);
  const byId = new Map(routes.map((route) => [route.id, route]));
  const activeApiRoutes = new Set(routes.flatMap((route) => route.apiRoutes || []));

  for (const [id, objectKind, path] of [
    ["compute-pools.list", "ComputeAllocation", "/console/compute/pools"],
    ["compute-allocations.list", "ComputeAllocation", "/console/compute"],
    ["compute-allocations.create", "ComputeAllocation", "/console/compute/new"],
    ["compute-allocations.detail", "ComputeAllocation", "/console/compute/:id"],
    ["storage.list", "StorageVolume", "/console/storage"],
    ["storage.create", "StorageVolume", "/console/storage/new"],
    ["storage.detail", "StorageVolume", "/console/storage/:id"],
    ["attachment.list", "StorageAttachment", "/console/attachments"],
    ["attachment.create", "StorageAttachment", "/console/attachments/new"],
    ["attachment.detail", "StorageAttachment", "/console/attachments/:id"],
    ["resources.relationships", "Workspace", "/console/resources/relationships"],
    ["workspace.list", "Workspace", "/console/workspaces"],
    ["workspace.create", "Workspace", "/console/workspaces/new"],
    ["workspace.detail", "Workspace", "/console/workspaces/:id"],
    ["admin.diagnostics", "FabricOperation", "/admin/diagnostics"],
    ["admin.e2e", "AdminAuditEvent", "/admin/e2e"],
    ["admin.cleanup", "AdminAuditEvent", "/admin/cleanup"]
  ]) {
    const route = byId.get(id);
    assert.ok(route, `missing current route ${id}`);
    assert.equal(route.path, path);
    assert.equal(route.objectKind, objectKind);
    assert.equal(route.status, "implemented");
    assert.equal(route.contractLifecycle, "current");
  }

  for (const apiRoute of [
    "GET /api/compute-pools",
    "GET /api/compute-allocations",
    "POST /api/compute-allocations",
    "GET /api/compute-allocations/:id",
    "POST /api/compute-allocations/:id/destroy",
    "POST /api/storage-volumes",
    "POST /api/storage-volumes/destroy",
    "POST /api/storage-attachments",
	    "POST /api/storage-attachments/detach",
	    "POST /api/operator/cleanup-workspace-access",
	    "POST /api/organizations",
	    "POST /api/organizations/members",
	    "POST /api/billing/reconciliation",
	    "POST /api/resources/:id/renew",
	    "POST /api/resources/:id/auto-renew",
	    "POST /api/users/disable",
    "POST /api/users/delete"
  ]) {
    assert.ok(activeApiRoutes.has(apiRoute), `${apiRoute} must be in current resource contract`);
  }
});

test("implemented routes name page, API intent, and service boundary", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract).filter((route) => route.status === "implemented");

  assert.ok(routes.length > 0, "contract should mark real routes as implemented");
  for (const route of routes) {
    assert.ok(route.id, `implemented route ${route.path} must name stable route id`);
    assert.ok(route.pageModule, `implemented route ${route.path} must name pageModule`);
    if (route.routeKind !== "static_content" && route.routeKind !== "external_integration") {
      assert.ok(route.apiClient, `implemented route ${route.path} must name apiClient`);
      assert.ok(Array.isArray(route.apiRoutes), `implemented route ${route.path} must list apiRoutes`);
      assert.ok(route.apiRoutes.length > 0, `implemented route ${route.path} must list at least one apiRoute`);
    }
    assert.ok(route.serviceBoundary, `implemented route ${route.path} must name serviceBoundary`);
    assert.equal(route.contractLifecycle, "current", `implemented route ${route.path} must be current`);
    assert.ok(route.capabilities.includes("read"), `implemented route ${route.path} must include read capability`);
  }
});

test("implemented commercial object routes declare object kind and object capabilities", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract).filter((route) => (
    route.status === "implemented"
    && ["business_object", "read_model"].includes(route.routeKind)
    && route.objectKind
  ));

  assert.ok(routes.length > 0, "contract should include implemented business routes");
  for (const route of routes) {
    assert.ok(route.objectKind, `${route.path} must declare objectKind`);
    assert.ok(route.capabilities.includes("read"), `${route.path} must declare read capability`);
    assert.ok(
      route.capabilities.some((capability) => ["list", "detail", "write", "action", "approve", "reject", "review", "evidence", "audit"].includes(capability)),
      `${route.path} must declare a business object capability`
    );
  }
});

test("paid and destructive route mutations declare commercial operation protocol", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract);
  const byId = new Map(routes.map((route) => [route.id, route]));

  for (const id of [
    "compute-allocations.create",
    "compute-allocations.detail",
    "storage.create",
    "storage.detail",
    "attachment.create",
    "attachment.detail",
    "workspace.create",
    "workspace.detail",
    "admin.cleanup"
  ]) {
    const route = byId.get(id);
    assert.ok(route, `missing route ${id}`);
    assert.equal(route.operationProtocol?.mutation, true, `${id} must declare mutation protocol`);
    assert.ok(["normal", "strong"].includes(route.operationProtocol?.confirmation), `${id} must declare confirmation level`);
    assert.equal(route.operationProtocol?.operationTimeline, true, `${id} must expose operation timeline`);
    assert.equal(route.operationProtocol?.failureVisible, true, `${id} must expose operation failures`);
  }

  assert.equal(byId.get("storage.detail").operationProtocol.dataLoss, true, "storage destroy route must declare data-loss risk");
  assert.equal(byId.get("storage.detail").operationProtocol.confirmText, "确认删除数据", "storage destroy must require strong Chinese confirmation text");
});

test("resource route contract declares dynamic fields, billing fields, and visible operation stages", async () => {
  const contract = await readJson(contractPath);
  const routes = expectedRoutesFromContract(contract);
  const byId = new Map(routes.map((route) => [route.id, route]));

  const computeList = byId.get("compute-allocations.list");
  const computeDetail = byId.get("compute-allocations.detail");
  const storageCreate = byId.get("storage.create");
  const storageDetail = byId.get("storage.detail");
  const attachmentCreate = byId.get("attachment.create");
  const workspaceCreate = byId.get("workspace.create");
  const workspaceDetail = byId.get("workspace.detail");
  const billingOverview = byId.get("billing.overview");

  for (const id of ["workspace.list", "workspace.create", "workspace.detail", "admin.cleanup"]) {
    assert.equal(byId.get(id).serviceBoundary, "WorkspaceLifecycleService", `${id} must use the Workspace lifecycle boundary`);
  }

  for (const field of ["ownerAccountId", "nodePoolId", "cvmInstanceId", "machineName", "nodeName", "privateIp", "billingStatus", "workspaceId"]) {
    assert.ok(computeList.dynamicFields?.includes(field), `compute list must declare ${field}`);
    assert.ok(computeDetail.dynamicFields?.includes(field), `compute detail must declare ${field}`);
  }
  for (const field of ["ownerAccountId", "storageId", "currentComputeAllocationId", "currentAttachmentId", "url", "runtime.status", "state"]) {
    assert.ok(workspaceDetail.dynamicFields?.includes(field), `workspace detail must declare ${field}`);
  }

  assert.deepEqual(computeDetail.operationProtocol.visibleStages, [
    "已提交",
    "云资源准备中",
    "余额扣款中",
    "月度权益已激活",
    "Runtime 部署中",
    "URL 可用"
  ]);
  assert.deepEqual(storageCreate.operationProtocol.visibleStages, [
    "已提交",
    "存储准备中",
    "余额扣款中",
    "月度权益已激活",
    "可挂载"
  ], "storage create must not claim compute Runtime or URL stages");
  assert.deepEqual(storageDetail.operationProtocol.visibleStages, [
    "已提交",
    "停止续费",
    "销毁存储",
    "已删除"
  ], "storage destroy stages must reflect data deletion risk");
  assert.deepEqual(attachmentCreate.operationProtocol.visibleStages, [
    "已提交",
    "挂载中",
    "可创建入口"
  ], "attachment create stages must lead to Workspace URL creation");
  assert.deepEqual(workspaceCreate.operationProtocol.visibleStages, [
    "已提交",
    "生成 URL",
    "URL 可用"
  ], "workspace create stages must describe URL entry creation");
  assert.deepEqual(computeDetail.operationProtocol.pollQuery, ["accountId"], "compute detail polling must preserve account scope");
  assert.ok(storageDetail.dynamicFields?.includes("providerResourceId"), "storage detail must expose provider storage handle");
  assert.ok(storageDetail.dynamicFields?.includes("ownerAccountId"), "storage detail must expose the owning account");
  for (const field of ["monthlyPriceCnyCents", "chargeUsdMicros", "paidThrough", "autoRenew"]) {
    assert.ok(storageDetail.dynamicFields?.includes(field), `storage detail must expose ${field}`);
  }
  assert.ok(storageDetail.dynamicFields?.includes("billingStatus"), "storage detail must expose billing status");

  for (const field of ["balanceUsdMicros", "balanceSource", "pricingVersion", "monthlyEntitlements", "paidThrough", "autoRenew", "manualReview"]) {
    assert.ok(billingOverview.dynamicFields?.includes(field), `billing overview must declare ${field}`);
  }
});
