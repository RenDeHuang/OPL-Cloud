import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const text = (path: string) => readFile(new URL(path, root), "utf8");
const json = async (path: string) => JSON.parse(await text(path));

function structJSONFields(source: string, typeName: string): string[] {
  const marker = `type ${typeName} struct {`;
  const start = source.indexOf(marker);
  assert.notEqual(start, -1, `missing ${typeName}`);
  const body = source.slice(start + marker.length, source.indexOf("\n}", start));
  return [...body.matchAll(/`json:"([^",]+)/g)].map((match) => match[1]);
}

test("Control Plane owns the launch identity and recovery authorization", async () => {
  const [contract, launchSource, routeSource] = await Promise.all([
    json("packages/contracts/opl-cloud-control-plane-launch-contract.json"),
    text("services/control-plane/internal/server/workspace_launch.go"),
    text("services/control-plane/internal/server/routes_admin.go")
  ]);

  assert.equal(contract.owner, "services/control-plane");
  assert.equal(contract.launchOperation.action, "workspace.launch.v2");
  assert.equal(contract.launchOperation.resultSchemaVersion, 2);
  assert.deepEqual(contract.launchOperation.identityFields, [
    "launchOperationId", "accountId", "ownerUserId", "workspaceId", "requestHash"
  ]);
  assert.equal(contract.stageDecision.fabricOperationBinding, "opl-cloud-fabric-launch-binding-contract.json");
  assert.equal(contract.recovery.operation, "continue_original_workspace_launch");
  assert.equal(contract.recovery.resourceIdentityInput, "forbidden_server_authoritative_readback_only");

  assert.match(launchSource, /workspaceLaunchAction\s*=\s*"workspace\.launch\.v2"/);
  assert.match(launchSource, /workspaceLaunchSchemaVersion\s*=\s*2/);
  for (const field of ["accountId", "ownerUserId", "workspaceId", "requestHash"]) {
    assert.ok(structJSONFields(launchSource, "workspaceLaunchOperation").includes(field), field);
  }
  for (const route of Object.values(contract.recovery.routes) as string[]) {
    const [method, path] = route.split(" ", 2);
    assert.match(routeSource, new RegExp(`${method} ${path.replace(/[{}]/g, "\\$&")}`));
  }
});

test("Fabric stage binding matches both typed DTOs and the current HTTP caller", async () => {
  const [contract, clientSource, fabricSource, serverSource, launchSource] = await Promise.all([
    json("packages/contracts/opl-cloud-fabric-launch-binding-contract.json"),
    text("services/control-plane/internal/clients/fabric.go"),
    text("services/fabric/internal/fabric/workspace_launch_readback.go"),
    text("services/fabric/internal/http/server.go"),
    text("services/control-plane/internal/server/workspace_launch.go")
  ]);

  assert.equal(contract.owner, "services/fabric");
  assert.deepEqual(contract.launchBinding.controlPlaneIdentityFields, [
    "launchOperationId", "launchRequestHash", "accountId", "workspaceId"
  ]);
  assert.deepEqual(contract.launchBinding.fabricStageIdentityFields, [
    "stage", "fabricRecordId", "fabricOperationId", "idempotencyKey", "requestHash"
  ]);
  assert.deepEqual(contract.launchBinding.expectedResourceBindingFields, [
    "action", "resourceKind", "resourceId", "accountId", "workspaceId"
  ]);

  const requestFields = structJSONFields(clientSource, "WorkspaceLaunchStageReadbackInput");
  const fabricRequestFields = structJSONFields(fabricSource, "WorkspaceLaunchStageReadbackInput");
  assert.deepEqual(requestFields, contract.stageReadbackApi.requestFields);
  assert.deepEqual(fabricRequestFields, requestFields);

  const proofFields = structJSONFields(clientSource, "WorkspaceLaunchStageReadbackProof");
  const fabricProofFields = structJSONFields(fabricSource, "WorkspaceLaunchStageReadbackProof");
  assert.deepEqual(proofFields, contract.stageReadbackApi.proofFields);
  assert.deepEqual(fabricProofFields, proofFields);

  assert.deepEqual(contract.stageReadbackApi.supportedStages, ["attachment", "secret", "runtime"]);
  assert.match(serverSource, /POST \/fabric\/workspace-launch-stage-readback\/proof/);
  assert.match(serverSource, /POST \/fabric\/workspace-launch-stage-readback\/converge/);

  const expectedStages = [
    ["compute", "create_compute_allocation", "compute_allocation", "<launchOperationId>:compute"],
    ["storage", "create_storage_volume", "storage_volume", "<launchOperationId>:storage"],
    ["attachment", "create_storage_attachment", "storage_attachment", "<attachmentOperationId>"],
    ["secret", "upsert_gateway_secret", "gateway_secret", "<workspaceOperationId>:secret:gateway-secret"],
    ["runtime", "create_workspace_runtime", "workspace_runtime", "<workspaceOperationId>:runtime"]
  ];
  assert.deepEqual(
    contract.stageOperations.map((stage: Record<string, string>) => [stage.stage, stage.action, stage.resourceKind, stage.idempotencyIdentity]),
    expectedStages
  );
  for (const [, action, resourceKind] of expectedStages) {
    assert.ok(launchSource.includes(`Action: "${action}", ResourceKind: "${resourceKind}"`), action);
  }
});
