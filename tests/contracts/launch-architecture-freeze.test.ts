import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const text = (path: string) => readFile(new URL(path, root), "utf8");
const json = async (path: string) => JSON.parse(await text(path));

async function optionalText(path: string): Promise<string | null> {
  try {
    return await text(path);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return null;
    throw error;
  }
}

test("launch architecture uses focused owners without the aggregate freeze", async () => {
  const [controlPlane, fabric] = await Promise.all([
    json("packages/contracts/opl-cloud-control-plane-launch-contract.json"),
    json("packages/contracts/opl-cloud-fabric-launch-binding-contract.json")
  ]);

  await assert.rejects(text("packages/contracts/opl-cloud-launch-freeze-contract.json"), { code: "ENOENT" });
  assert.equal(controlPlane.stageDecision.fabricOperationBinding, "opl-cloud-fabric-launch-binding-contract.json");
  assert.equal(fabric.owner, "services/fabric");
  assert.deepEqual(fabric.stageRequestHash.payloadFields, [
    "launchRequestHash", "action", "packageId", "sizeGb", "imageDigest", "resources"
  ]);
});

test("successor Reconciler durably reserves each stage before mutation", async () => {
  const contract = await json("packages/contracts/opl-cloud-control-plane-launch-contract.json");
  const boundary = contract.stageDecision.attemptPersistence;
  const source = await optionalText(boundary.reconcilerSource);

  assert.deepEqual(boundary.goFields, [
    "Attempted", "Confirmed", "Unknown", "Max", "IdempotencyKey", "PendingReadbacks", "MaxPendingReadbacks"
  ]);
  assert.equal(boundary.maxPerStage, 1);
  assert.deepEqual(boundary.reservationOrder, [
    "increment_attempted_and_set_idempotency_key", "persist_exact_result_cas", "invoke_stage_mutation"
  ]);

  // L1 is integrated before the L3 source. The structural assertions activate
  // on the same test as soon as the successor Reconciler is present.
  if (source === null) return;

  assert.match(source, /type workspaceLaunchStageAttempt struct \{[\s\S]*Attempted\s+int\s+`json:"attempted"`[\s\S]*Max\s+int\s+`json:"max"`[\s\S]*IdempotencyKey\s+string\s+`json:"idempotencyKey,omitempty"`/);
  assert.match(source, /attempts\[stage\]\s*=\s*workspaceLaunchStageAttempt\{Max:\s*1,\s*MaxPendingReadbacks:\s*workspaceLaunchLegacyV3AuthoritativeReadBudget\}/);
  assert.match(source, /attempt\.Attempted\s*==\s*attempt\.Max/);
  assert.doesNotMatch(source, /attempt\.Max\+\+|attempt\.Attempted\s*=\s*0/);
  assert.doesNotMatch(source, /\bChargeAttempted\b/);

  const attempted = source.indexOf("attempt.Attempted++");
  const idempotency = source.indexOf("attempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey", attempted);
  const persisted = source.indexOf("reserved, err := r.persist", idempotency);
  const mutated = source.indexOf("r.adapter.MutateStage", persisted);
  assert.ok(attempted >= 0 && attempted < idempotency && idempotency < persisted && persisted < mutated);

  for (const path of boundary.stageAdapterSources) {
    assert.notEqual(await optionalText(path), null, path);
  }
});

test("Control Plane and Fabric tests consume the same owner hash vectors", async () => {
  const consumers = [
    "services/fabric/internal/fabric/workspace_launch_stage_test.go",
    "services/control-plane/internal/server/workspace_launch_reconciler_test.go"
  ];
  for (const path of consumers) {
    const source = await optionalText(path);
    if (source === null) continue;
    assert.match(source, /opl-cloud-fabric-launch-binding-contract\.json/, path);
  }

  for (const [path, helper] of [
    ["services/fabric/internal/fabric/workspace_launch_stage_test.go", "workspaceLaunchCallerRequestHash"],
    ["services/fabric/internal/http/server_test.go", "workspaceLaunchStageHTTPHash"]
  ]) {
    const source = await optionalText(path);
    if (source !== null) assert.equal(source.includes(`func ${helper}`), false, `${path}:${helper}`);
  }
});
