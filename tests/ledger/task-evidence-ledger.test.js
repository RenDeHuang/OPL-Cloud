import assert from "node:assert/strict";
import test from "node:test";

import {
  appendTaskEvidenceReceipt,
  createTaskEvidenceReceipt,
  filterTaskEvidenceReceipts
} from "../../packages/ledger/src/task-evidence.js";

test("task evidence receipt v1 records plan, approval, environment, refs, review, and continuation", () => {
  const state = { evidenceLedger: [] };
  const receipt = createTaskEvidenceReceipt({
    state,
    accountId: "pi-alpha",
    workspaceId: "ws-alpha",
    taskId: "task-literature-review",
    actor: { type: "user", id: "usr-ada" },
    plan: { goal: "review three papers", expectedOutput: "annotated memo" },
    approval: { status: "approved", approvedBy: "usr-ada", approvedAt: "2026-07-02T08:00:00.000Z" },
    environment: { runtimeProvider: "tencent-tke", image: "one-person-lab-app:20260702" },
    inputRefs: [{ type: "file", uri: "opl://workspace/ws-alpha/input/papers.bib" }],
    executionRefs: [{ type: "command", uri: "opl://workspace/ws-alpha/runs/run-1", digest: "sha256:abc" }],
    outputRefs: [{ type: "artifact", uri: "opl://workspace/ws-alpha/output/memo.md" }],
    reviewResults: [{ status: "pass", reviewer: "usr-ada", notes: "ready" }],
    continuation: { action: "continue_task", uri: "opl://workspace/ws-alpha/tasks/task-literature-review" }
  });

  appendTaskEvidenceReceipt(state, receipt);

  assert.equal(receipt.type, "task.evidence.v1");
  assert.match(receipt.id, /^task-receipt-/);
  assert.equal(receipt.taskId, "task-literature-review");
  assert.equal(receipt.plan.goal, "review three papers");
  assert.equal(receipt.approval.status, "approved");
  assert.equal(receipt.environment.runtimeProvider, "tencent-tke");
  assert.equal(receipt.inputRefs[0].uri, "opl://workspace/ws-alpha/input/papers.bib");
  assert.equal(receipt.executionRefs[0].digest, "sha256:abc");
  assert.equal(receipt.outputRefs[0].type, "artifact");
  assert.equal(receipt.reviewResults[0].status, "pass");
  assert.equal(receipt.continuation.action, "continue_task");
  assert.equal(state.evidenceLedger.length, 1);
});

test("task evidence helper filters append-only receipts by account, workspace, and task", () => {
  const state = { evidenceLedger: [] };
  appendTaskEvidenceReceipt(state, createTaskEvidenceReceipt({
    state,
    accountId: "pi-alpha",
    workspaceId: "ws-alpha",
    taskId: "task-a",
    plan: { goal: "A" },
    approval: { status: "approved" },
    environment: { runtimeProvider: "local-docker" }
  }));
  appendTaskEvidenceReceipt(state, createTaskEvidenceReceipt({
    state,
    accountId: "pi-alpha",
    workspaceId: "ws-alpha",
    taskId: "task-b",
    plan: { goal: "B" },
    approval: { status: "approved" },
    environment: { runtimeProvider: "local-docker" }
  }));
  appendTaskEvidenceReceipt(state, createTaskEvidenceReceipt({
    state,
    accountId: "pi-beta",
    workspaceId: "ws-beta",
    taskId: "task-a",
    plan: { goal: "Other" },
    approval: { status: "approved" },
    environment: { runtimeProvider: "local-docker" }
  }));

  assert.deepEqual(
    filterTaskEvidenceReceipts(state, { accountId: "pi-alpha", workspaceId: "ws-alpha" }).map((item) => item.taskId),
    ["task-a", "task-b"]
  );
  assert.deepEqual(
    filterTaskEvidenceReceipts(state, { accountId: "pi-alpha", taskId: "task-a" }).map((item) => item.workspaceId),
    ["ws-alpha"]
  );
});

test("task evidence receipt fails closed when core provenance fields are missing", () => {
  assert.throws(
    () => createTaskEvidenceReceipt({
      accountId: "pi-alpha",
      taskId: "task-missing-plan",
      approval: { status: "approved" },
      environment: { runtimeProvider: "local-docker" }
    }),
    /task_evidence_plan_required/
  );
  assert.throws(
    () => createTaskEvidenceReceipt({
      accountId: "pi-alpha",
      taskId: "task-missing-environment",
      plan: { goal: "run" },
      approval: { status: "approved" }
    }),
    /task_evidence_environment_required/
  );
});
