import assert from "node:assert/strict";
import test from "node:test";

import { appendEvidenceReceipt, createEvidenceReceipt } from "../../packages/ledger/src/evidence-ledger.js";
import {
  createCurrentTestService,
  provisionWorkspace
} from "../helpers/current-resource-chain.js";

test("evidence ledger records inspectable resource-chain Workspace receipts separately from billing ledger", async () => {
  const service = createCurrentTestService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });

  const { compute, storage, attachment, workspace } = await provisionWorkspace(service, {
    accountId: "pi-alpha",
    userId: "usr-alpha",
    workspaceName: "Evidence Lab",
    packageId: "basic"
  });
  await service.deleteWorkspaceToken({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });

  const state = await service.getState("pi-alpha");
  assert.deepEqual(state.evidenceLedger.map((entry) => entry.type), [
    "workspace.created",
    "workspace.access_token_deleted"
  ]);

  const createReceipt = state.evidenceLedger[0];
  assert.equal(createReceipt.accountId, "pi-alpha");
  assert.equal(createReceipt.workspaceId, workspace.id);
  assert.equal(createReceipt.environment.runtimeProvider, "test-provider");
  assert.equal(createReceipt.resourceRefs.serverId, workspace.server.id);
  assert.equal(createReceipt.resourceRefs.storageId, workspace.disk.id);
  assert.equal(createReceipt.resourceRefs.urlTokenMode, "long_lived_url_token");
  assert.deepEqual(createReceipt.continuation, {
    action: "open_workspace_url",
    uri: workspace.url
  });
  assert.equal(createReceipt.billingRefs.some((entry) => entry.type === "compute_hold"), true);
  assert.equal(createReceipt.billingRefs.some((entry) => entry.type === "storage_hold"), true);
  assert.equal(createReceipt.billingRefs.some((entry) => entry.type === "storage_attached"), true);
  assert.equal(state.billingLedger.some((entry) => entry.id === createReceipt.id), false);
});

test("evidence ledger helper appends deterministic receipt ids without using billing ledger sequence", () => {
  const state = { evidenceLedger: [], billingLedger: [{ id: "billing-1" }] };
  const receipt = createEvidenceReceipt({
    state,
    type: "workspace.reviewed",
    accountId: "pi-alpha",
    workspaceId: "ws-alpha",
    actor: { type: "user", id: "usr-ada" },
    plan: { goal: "review output" },
    approval: { status: "approved", approvedBy: "usr-ada" },
    environment: { runtimeProvider: "test-provider" },
    inputRefs: [{ type: "file", uri: "opl://input.md" }],
    outputRefs: [{ type: "file", uri: "opl://output.md" }],
    reviewResults: [{ status: "pass", reviewer: "human" }],
    continuation: { action: "continue_task", uri: "opl://task/next" }
  });

  appendEvidenceReceipt(state, receipt);

  assert.match(receipt.id, /^receipt-/);
  assert.equal(state.evidenceLedger.length, 1);
  assert.equal(state.billingLedger.length, 1);
  assert.equal(state.evidenceLedger[0].type, "workspace.reviewed");
});
