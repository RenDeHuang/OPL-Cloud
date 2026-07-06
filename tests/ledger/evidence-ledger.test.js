import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { appendEvidenceReceipt, createEvidenceReceipt } from "../../packages/ledger/src/evidence-ledger.js";
import { MemoryStore } from "../../packages/console/src/store.js";
import { createFakeRuntimeProvider } from "../helpers/fake-runtime-provider.js";

const TEST_PRICING = {
  computeHourly: { basic: 1, pro: 4 },
  storageGbMonth: 0.2,
  markup: 0.2
};

function createTestService() {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: createFakeRuntimeProvider({
      name: "test-provider",
      workspaceUrl({ workspaceId, token }) {
        return `https://workspace.example.com/w/${workspaceId}?token=${token}`;
      }
    }),
    pricing: TEST_PRICING
  });
}

function withoutWorkspaceToken(url) {
  const parsed = new URL(url);
  return `${parsed.origin}${parsed.pathname.replace(/\/$/, "")}`;
}

async function createWorkspaceEntry(service, { accountId, workspaceName, packageId = "basic" }) {
  const storage = await service.createStorageVolume({ accountId, packageId, name: `${workspaceName} storage` });
  const compute = await service.createComputeAllocation({ accountId, packageId, name: `${workspaceName} compute` });
  await service.processPendingResourceProvisioning({ limit: 1 });
  const attachment = await service.attachStorage({
    accountId,
    computeAllocationId: compute.id,
    storageId: storage.id,
    mountPath: "/data"
  });
  return service.createWorkspace({ accountId, workspaceName, attachmentId: attachment.id });
}

test("evidence ledger records inspectable Workspace receipts separately from billing ledger", async () => {
  const service = createTestService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });

  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Evidence Lab"
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
  assert.deepEqual(createReceipt.plan, {
    workspaceName: "Evidence Lab",
    packageId: "basic",
    computeProfile: "2c4g",
    storageGb: 10
  });
  assert.equal(createReceipt.approval.status, "implicit_console_policy");
  assert.equal(createReceipt.environment.runtimeProvider, "test-provider");
  assert.equal(createReceipt.resourceRefs.serverId, workspace.server.id);
  assert.equal(createReceipt.resourceRefs.storageId, workspace.disk.id);
  assert.equal(createReceipt.resourceRefs.urlTokenMode, "long_lived_url_token");
  assert.deepEqual(createReceipt.billingRefs.map((entry) => entry.type), [
    "storage_hold",
    "compute_hold",
    "storage_attached",
    "workspace_entry_created"
  ]);
  assert.equal(createReceipt.continuation.action, "open_workspace_url");
  assert.equal(createReceipt.continuation.workspaceId, workspace.id);
  assert.equal(createReceipt.continuation.tokenVersion, 1);
  assert.equal(createReceipt.continuation.redactedUrl, withoutWorkspaceToken(workspace.url));
  assert.equal(JSON.stringify(createReceipt).includes("token="), false);
  assert.equal(JSON.stringify(createReceipt.continuation).includes(workspace.access.token), false);
  assert.notEqual(createReceipt.continuation.redactedUrl, workspace.url);
});

test("workspace token reset evidence stores only redacted URL metadata", async () => {
  const service = createTestService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });

  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Reset Evidence Lab"
  });
  const resetWorkspace = await service.resetWorkspaceToken({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });

  const state = await service.getState("pi-alpha");
  const resetReceipt = state.evidenceLedger.find((entry) => entry.type === "workspace.access_token_reset");
  assert.ok(resetReceipt);
  assert.deepEqual(resetReceipt.continuation, {
    action: "open_workspace_url",
    workspaceId: workspace.id,
    tokenVersion: 2,
    redactedUrl: withoutWorkspaceToken(resetWorkspace.url)
  });
  assert.equal(JSON.stringify(resetReceipt).includes("share_"), false);
  assert.equal(JSON.stringify(resetReceipt).includes("token="), false);
  assert.equal(JSON.stringify(resetReceipt).includes(resetWorkspace.access.token), false);
  assert.notEqual(resetReceipt.continuation.redactedUrl, resetWorkspace.url);
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
