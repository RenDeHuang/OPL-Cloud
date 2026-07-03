import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import test from "node:test";

import { createRequestHandler } from "../../packages/console/api/server.js";

async function listen(handler) {
  const server = createServer(handler);
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  return {
    origin: `http://127.0.0.1:${address.port}`,
    close: () => new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
  };
}

async function postJson(origin, path, body) {
  const response = await fetch(`${origin}${path}`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body)
  });
  return { response, payload: await response.json() };
}

test("management API exposes organization, user, membership, and management state endpoints", async () => {
  const calls = [];
  const appService = {
    async createOrganization(input) {
      calls.push(["createOrganization", input]);
      return { id: input.organizationId, name: input.name, billingAccountId: input.organizationId };
    },
    async createUser(input) {
      calls.push(["createUser", input]);
      return { id: input.userId, email: input.email, name: input.name };
    },
    async addOrganizationMember(input) {
      calls.push(["addOrganizationMember", input]);
      return { id: "membership-1", ...input, status: "active" };
    },
    async managementState(input) {
      calls.push(["managementState", input]);
      return {
        organization: { id: input.organizationId },
        users: [{ id: "usr-ada" }],
        memberships: [{ organizationId: input.organizationId, userId: "usr-ada" }],
        billingAccount: { id: input.organizationId, balance: 250, frozen: 202.16 },
        packages: [{ id: "basic" }],
        workspaces: []
      };
    }
  };
  const { origin, close } = await listen(createRequestHandler({ appService }));
  try {
    const org = await postJson(origin, "/api/organizations", {
      organizationId: "org-lab",
      name: "OPL Lab"
    });
    assert.equal(org.response.status, 200);
    assert.equal(org.payload.id, "org-lab");

    const user = await postJson(origin, "/api/users", {
      userId: "usr-ada",
      email: "ada@example.com",
      name: "Ada"
    });
    assert.equal(user.response.status, 200);
    assert.equal(user.payload.id, "usr-ada");

    const membership = await postJson(origin, "/api/organizations/members", {
      organizationId: "org-lab",
      userId: "usr-ada",
      role: "owner"
    });
    assert.equal(membership.response.status, 200);
    assert.equal(membership.payload.status, "active");

    const stateResponse = await fetch(`${origin}/api/management/state?organizationId=org-lab`);
    const state = await stateResponse.json();
    assert.equal(stateResponse.status, 200);
    assert.equal(state.organization.id, "org-lab");
    assert.deepEqual(calls.map(([name]) => name), [
      "createOrganization",
      "createUser",
      "addOrganizationMember",
      "managementState"
    ]);
  } finally {
    await close();
  }
});

test("storage backup API routes to backup, restore, and retention operations", async () => {
  const calls = [];
  const appService = {
    async createStorageBackup(input) {
      calls.push(["createStorageBackup", input]);
      return { id: "backup-1", status: "available", workspaceId: input.workspaceId };
    },
    async restoreWorkspaceFromBackup(input) {
      calls.push(["restoreWorkspaceFromBackup", input]);
      return { id: "ws-restored", restoredFromBackupId: input.backupId };
    },
    async pruneStorageBackups(input) {
      calls.push(["pruneStorageBackups", input]);
      return { deletedBackupIds: ["backup-old"] };
    }
  };
  const { origin, close } = await listen(createRequestHandler({ appService }));
  try {
    const backup = await postJson(origin, "/api/workspaces/storage-backups", {
      accountId: "pi-alpha",
      workspaceId: "ws-alpha",
      reason: "manual"
    });
    assert.equal(backup.response.status, 200);
    assert.equal(backup.payload.id, "backup-1");

    const restored = await postJson(origin, "/api/workspaces/restore-storage-backup", {
      accountId: "pi-alpha",
      backupId: "backup-1",
      workspaceName: "Restored Lab",
      packageId: "basic"
    });
    assert.equal(restored.response.status, 200);
    assert.equal(restored.payload.restoredFromBackupId, "backup-1");

    const pruned = await postJson(origin, "/api/workspaces/prune-storage-backups", {
      accountId: "pi-alpha",
      workspaceId: "ws-alpha"
    });
    assert.equal(pruned.response.status, 200);
    assert.deepEqual(pruned.payload.deletedBackupIds, ["backup-old"]);

    assert.deepEqual(calls.map(([name]) => name), [
      "createStorageBackup",
      "restoreWorkspaceFromBackup",
      "pruneStorageBackups"
    ]);
  } finally {
    await close();
  }
});

test("billing reconciliation API records guard reports before provisioning", async () => {
  const calls = [];
  const appService = {
    async recordBillingReconciliation(input) {
      calls.push(["recordBillingReconciliation", input]);
      return {
        id: "recon-1",
        guard: {
          blockNewWorkspaces: true,
          reason: "tencent_bill_reconciliation_failed"
        }
      };
    },
    async createWorkspace() {
      throw new Error("billing_reconciliation_guard_blocked:tencent_bill_reconciliation_failed");
    }
  };
  const { origin, close } = await listen(createRequestHandler({ appService }));
  try {
    const recorded = await postJson(origin, "/api/billing/reconciliation", {
      report: {
        ok: false,
        generatedAt: "2026-07-02T00:00:00.000Z",
        mismatches: [{ workspaceId: "ws-alpha" }]
      }
    });
    assert.equal(recorded.response.status, 200);
    assert.equal(recorded.payload.guard.blockNewWorkspaces, true);

    const blocked = await postJson(origin, "/api/workspaces", {
      accountId: "pi-alpha",
      workspaceName: "Blocked Lab",
      packageId: "basic"
    });
    assert.equal(blocked.response.status, 400);
    assert.equal(blocked.payload.error, "billing_reconciliation_guard_blocked:tencent_bill_reconciliation_failed");
    assert.deepEqual(calls.map(([name]) => name), ["recordBillingReconciliation"]);
  } finally {
    await close();
  }
});

test("request usage API routes gateway usage into billing service", async () => {
  const calls = [];
  const appService = {
    async recordRequestUsage(input) {
      calls.push(["recordRequestUsage", input]);
      return {
        id: "usage-request-1",
        userId: "usr-pi-alpha",
        accountId: "pi-alpha",
        ...input
      };
    }
  };
  const { origin, close } = await listen(createRequestHandler({ appService }));
  try {
    const usage = await postJson(origin, "/api/billing/request-usage", {
      accountId: "pi-alpha",
      workspaceId: "ws-alpha",
      requestId: "req-alpha",
      provider: "openai",
      model: "gpt-5",
      inputTokens: 1000,
      outputTokens: 500,
      amount: 0.25,
      sourceEventId: "gateway_req_alpha"
    });

    assert.equal(usage.response.status, 200);
    assert.equal(usage.payload.id, "usage-request-1");
    assert.deepEqual(calls, [
      ["recordRequestUsage", {
        accountId: "pi-alpha",
        workspaceId: "ws-alpha",
        requestId: "req-alpha",
        provider: "openai",
        model: "gpt-5",
        inputTokens: 1000,
        outputTokens: 500,
        amount: 0.25,
        sourceEventId: "gateway_req_alpha"
      }]
    ]);
  } finally {
    await close();
  }
});

test("support ticket API persists Lab Owner tickets through the Console service", async () => {
  const calls = [];
  const appService = {
    async supportTickets(input) {
      calls.push(["supportTickets", input]);
      return [{ id: "ticket-1", accountId: input.accountId, title: "Workspace access" }];
    },
    async createSupportTicket(input) {
      calls.push(["createSupportTicket", input]);
      return { id: "ticket-2", status: "open", ...input };
    }
  };
  const { origin, close } = await listen(createRequestHandler({ appService }));
  try {
    const listResponse = await fetch(`${origin}/api/support/tickets?accountId=pi-alpha`);
    const list = await listResponse.json();
    assert.equal(listResponse.status, 200);
    assert.deepEqual(list.tickets.map((ticket) => ticket.id), ["ticket-1"]);

    const created = await postJson(origin, "/api/support/tickets", {
      accountId: "pi-alpha",
      title: "Workspace access",
      category: "Workspace",
      priority: "normal",
      description: "Workspace URL returns 403."
    });
    assert.equal(created.response.status, 200);
    assert.equal(created.payload.id, "ticket-2");
    assert.deepEqual(calls, [
      ["supportTickets", { accountId: "pi-alpha" }],
      ["createSupportTicket", {
        accountId: "pi-alpha",
        title: "Workspace access",
        category: "Workspace",
        priority: "normal",
        description: "Workspace URL returns 403.",
        userId: "",
        author: ""
      }]
    ]);
  } finally {
    await close();
  }
});

test("task evidence API records and queries Ledger task receipts", async () => {
  const calls = [];
  const appService = {
    async recordTaskEvidenceReceipt(input) {
      calls.push(["recordTaskEvidenceReceipt", input]);
      return {
        id: "task-receipt-1",
        type: "task.evidence.v1",
        accountId: input.accountId,
        workspaceId: input.workspaceId,
        taskId: input.taskId,
        executionRefs: input.executionRefs
      };
    },
    async taskEvidenceReceipts(input) {
      calls.push(["taskEvidenceReceipts", input]);
      return [
        {
          id: "task-receipt-1",
          type: "task.evidence.v1",
          accountId: input.accountId,
          workspaceId: input.workspaceId,
          taskId: input.taskId
        }
      ];
    }
  };
  const { origin, close } = await listen(createRequestHandler({ appService }));
  try {
    const recorded = await postJson(origin, "/api/ledger/task-receipts", {
      accountId: "pi-alpha",
      workspaceId: "ws-alpha",
      taskId: "task-alpha",
      plan: { goal: "draft report" },
      approval: { status: "approved" },
      environment: { runtimeProvider: "tencent-tke" },
      executionRefs: [{ type: "run", uri: "opl://run/1" }]
    });
    assert.equal(recorded.response.status, 200);
    assert.equal(recorded.payload.type, "task.evidence.v1");
    assert.equal(recorded.payload.executionRefs[0].uri, "opl://run/1");

    const queryResponse = await fetch(`${origin}/api/ledger/task-receipts?accountId=pi-alpha&workspaceId=ws-alpha&taskId=task-alpha`);
    const queried = await queryResponse.json();
    assert.equal(queryResponse.status, 200);
    assert.equal(queried[0].id, "task-receipt-1");
    assert.deepEqual(calls.map(([name]) => name), ["recordTaskEvidenceReceipt", "taskEvidenceReceipts"]);
  } finally {
    await close();
  }
});
