import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { appendEvidenceReceipt, createEvidenceReceipt } from "../../packages/ledger/src/evidence-ledger.js";
import { MemoryStore } from "../../packages/console/src/store.js";

const TEST_PRICING = {
  computeHourly: { basic: 1, pro: 4 },
  storageGbMonth: 0.2,
  markup: 0.2
};

function runtimeFixture({ workspaceId, workspaceName, packagePlan, token }) {
  return {
    provider: "test-provider",
    server: {
      id: `server-${workspaceId}`,
      status: "running",
      billingStatus: "active",
      spec: packagePlan.server
    },
    docker: {
      id: `docker-${workspaceId}`,
      image: "test-image",
      status: "running"
    },
    disk: {
      id: `disk-${workspaceId}`,
      status: "attached_retained",
      billingStatus: "active",
      sizeGb: packagePlan.diskGb,
      mountPath: "/data"
    },
    url: `https://workspace.example.com/w/${workspaceId}?token=${token}`,
    slug: workspaceName
  };
}

function createTestService() {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: {
      name: "test-provider",
      async createWorkspaceRuntime(input) {
        return runtimeFixture(input);
      },
      async stopServer({ workspace }) {
        return { ...workspace.server, status: "stopped", billingStatus: "stopped" };
      }
    },
    pricing: TEST_PRICING
  });
}

test("evidence ledger records inspectable Workspace receipts separately from billing ledger", async () => {
  const service = createTestService();
  await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });

  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Evidence Lab",
    packageId: "basic"
  });
  await service.stopServer({
    accountId: "pi-alpha",
    workspaceId: workspace.id,
    confirm: true
  });
  await service.deleteWorkspaceToken({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });

  const state = await service.getState("pi-alpha");
  assert.deepEqual(state.evidenceLedger.map((entry) => entry.type), [
    "workspace.created",
    "workspace.compute_stopped",
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
    "compute_hold",
    "storage_hold",
    "compute_debit",
    "storage_debit"
  ]);
  assert.equal(createReceipt.continuation.action, "open_workspace_url");
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
