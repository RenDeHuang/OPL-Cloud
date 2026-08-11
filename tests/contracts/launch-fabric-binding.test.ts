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

test("Fabric launch binding freezes only the typed successor seam", async () => {
  const contract = await json("packages/contracts/opl-cloud-fabric-launch-binding-contract.json");

  assert.equal(contract.owner, "services/fabric");
  assert.deepEqual(contract.workspaceLaunchApi, {
    preflightRoute: "POST /fabric/workspace-launches/preflight",
    stageReadRoute: "POST /fabric/workspace-launches/stages/read",
    stageEnsureRoute: "POST /fabric/workspace-launches/stages/ensure",
    bindingType: "WorkspaceLaunchStageBinding",
    ensureIdempotencyHeader: "Idempotency-Key equals binding.idempotencyKey"
  });
  assert.deepEqual(contract.launchBinding.fields, [
    "schemaVersion", "launchOperationId", "accountId", "workspaceId", "stage", "action",
    "fabricOperationId", "idempotencyKey", "requestHash", "expectedResourceBinding"
  ]);
  assert.equal(
    contract.launchBinding.writeProtocol,
    "control_plane_submits_complete_binding_and_fabric_persists_it_before_provider_write"
  );
  assert.equal(
    contract.launchBinding.readProtocol,
    "lookup_by_explicit_fabricOperationId_and_require_exact_binding_match"
  );
  assert.deepEqual(contract.readback.matchFields, contract.launchBinding.fields);
  assert.deepEqual(contract.readback.forbiddenInference, [
    "idempotency_suffix", "unscoped_operation_list", "provider_tag", "provider_resource_name"
  ]);

  const expectedStages = [
    ["ensure_compute_allocation", "ensure_compute_allocation"],
    ["storage", "ensure_storage"],
    ["attachment", "ensure_attachment"],
    ["secret", "ensure_gateway_secret"],
    ["runtime", "ensure_runtime"]
  ];
  assert.deepEqual(
    contract.stageOperations.map((stage: Record<string, string>) => [stage.stage, stage.action]),
    expectedStages
  );

  const serialized = JSON.stringify(contract);
  for (const legacy of [
    "stageReadbackApi", "workspace-launch-stage-readback", "proofRoute", "convergeRoute",
    "fabricRecordId", "sub2apiMutationCount", "tencentMutationCount", "kubernetesMutationCount",
    "fabricOperationMutationCount", "idempotencyIdentity", "<launchOperationId>"
  ]) {
    assert.equal(serialized.includes(legacy), false, legacy);
  }
});
