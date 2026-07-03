import assert from "node:assert/strict";
import test from "node:test";

import {
  createCurrentTestService,
  provisionWorkspace
} from "../helpers/current-resource-chain.js";

test("Console records and queries task evidence receipts without mixing them into billing ledger", async () => {
  const service = createCurrentTestService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const { workspace } = await provisionWorkspace(service, {
    accountId: "pi-alpha",
    userId: "usr-alpha",
    workspaceName: "Task Evidence Lab",
    packageId: "basic"
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
  const service = createCurrentTestService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  await service.manualTopUp({ accountId: "pi-beta", amount: 300, reason: "owner_credit" });
  const { workspace } = await provisionWorkspace(service, {
    accountId: "pi-alpha",
    userId: "usr-alpha",
    workspaceName: "Owned Lab",
    packageId: "basic"
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
