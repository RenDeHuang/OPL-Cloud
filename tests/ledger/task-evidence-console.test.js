import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";

const TEST_PRICING = {
  computeHourly: { basic: 1, pro: 4 },
  storageGbMonth: 0.2,
  markup: 0.2
};

function createService() {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: {
      name: "test-provider",
      workspaceUrl({ workspaceId, token }) {
        return `https://workspace.example.com/w/${workspaceId}?token=${token}`;
      }
    },
    pricing: TEST_PRICING
  });
}

async function createWorkspaceEntry(service, { accountId, workspaceName, packageId = "basic" }) {
  const storage = await service.createStorageVolume({ accountId, packageId, name: `${workspaceName} storage` });
  const compute = await service.createComputeAllocation({ accountId, packageId, name: `${workspaceName} compute` });
  const attachment = await service.attachStorage({
    accountId,
    computeAllocationId: compute.id,
    storageId: storage.id,
    mountPath: "/data"
  });
  return service.createWorkspace({ accountId, workspaceName, attachmentId: attachment.id });
}

test("Console records and queries task evidence receipts without mixing them into billing ledger", async () => {
  const service = createService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Task Evidence Lab"
  });

  const receipt = await service.recordTaskEvidenceReceipt({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    taskId: "task-rca-1",
    actor: { type: "user", id: "usr-ada" },
    plan: { goal: "produce RCA draft" },
    approval: { status: "approved", approvedBy: "usr-ada" },
    environment: { runtimeProvider: "test-provider", image: "test-image" },
    inputRefs: [{ type: "file", uri: "opl://input.md" }],
    executionRefs: [{ type: "run", uri: "opl://run/1" }],
    outputRefs: [{ type: "file", uri: "opl://output.md" }],
    reviewResults: [{ status: "pass", reviewer: "usr-ada" }],
    continuation: { action: "continue_task", uri: "opl://task/task-rca-1" }
  });

  assert.equal(receipt.type, "task.evidence.v1");
  const receipts = await service.taskEvidenceReceipts({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    taskId: "task-rca-1"
  });
  assert.equal(receipts.length, 1);
  assert.equal(receipts[0].executionRefs[0].uri, "opl://run/1");

  const state = await service.getState("pi-alpha");
  assert.equal(state.evidenceLedger.some((entry) => entry.id === receipt.id), true);
  assert.equal(state.billingLedger.some((entry) => entry.id === receipt.id), false);
});

test("Console task evidence receipt enforces workspace ownership", async () => {
  const service = createService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  await service.manualTopUp({ accountId: "pi-beta", amount: 250, reason: "owner_credit" });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Owned Lab"
  });

  await assert.rejects(
    service.recordTaskEvidenceReceipt({
      accountId: "pi-beta",
      workspaceId: workspace.id,
      taskId: "task-wrong-account",
      plan: { goal: "tamper" },
      approval: { status: "approved" },
      environment: { runtimeProvider: "test-provider" }
    }),
    /workspace_not_found/
  );
});
