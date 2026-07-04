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
    async disableUser(input) {
      calls.push(["disableUser", input]);
      return { id: input.userId, status: "disabled" };
    },
    async deleteUser(input) {
      calls.push(["deleteUser", input]);
      return { id: input.userId, status: "deleted" };
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

    const disabled = await postJson(origin, "/api/users/disable", {
      userId: "usr-ada",
      reason: "security_review"
    });
    assert.equal(disabled.response.status, 200);
    assert.equal(disabled.payload.status, "disabled");

    const deleted = await postJson(origin, "/api/users/delete", {
      userId: "usr-ada",
      reason: "account_closed"
    });
    assert.equal(deleted.response.status, 200);
    assert.equal(deleted.payload.status, "deleted");

    const stateResponse = await fetch(`${origin}/api/management/state?organizationId=org-lab`);
    const state = await stateResponse.json();
    assert.equal(stateResponse.status, 200);
    assert.equal(state.organization.id, "org-lab");
    assert.deepEqual(calls.map(([name]) => name), [
      "createOrganization",
      "createUser",
      "addOrganizationMember",
      "disableUser",
      "deleteUser",
      "managementState"
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
    async createComputeAllocation() {
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

    const blocked = await postJson(origin, "/api/compute-allocations", {
      accountId: "pi-alpha",
      packageId: "basic",
      name: "Blocked compute"
    });
    assert.equal(blocked.response.status, 400);
    assert.equal(blocked.payload.error, "billing_reconciliation_guard_blocked:tencent_bill_reconciliation_failed");
    assert.deepEqual(calls.map(([name]) => name), ["recordBillingReconciliation"]);
  } finally {
    await close();
  }
});

test("resource mutation errors expose safe provider details without raw provider data", async () => {
  const appService = {
    async createComputeAllocation() {
      const error = new Error("tencent_permission_denied");
      error.safeMessage = "CAM denied ScaleNodePool";
      error.providerRequestId = "req-denied";
      error.retryable = false;
      error.providerData = {
        action: "create_compute_allocation",
        secretId: "should-not-leak",
        rawTencentPayload: { credential: "should-not-leak" }
      };
      throw error;
    }
  };
  const { origin, close } = await listen(createRequestHandler({ appService }));
  try {
    const failed = await postJson(origin, "/api/compute-allocations", {
      accountId: "pi-alpha",
      packageId: "basic",
      name: "Denied compute"
    });

    assert.equal(failed.response.status, 400);
    assert.equal(failed.payload.error, "tencent_permission_denied");
    assert.equal(failed.payload.safeMessage, "CAM denied ScaleNodePool");
    assert.equal(failed.payload.providerRequestId, "req-denied");
    assert.equal(failed.payload.retryable, false);
    assert.deepEqual(failed.payload.providerData, undefined);
    assert.deepEqual(failed.payload.provider, {
      requestId: "req-denied",
      retryable: false
    });
  } finally {
    await close();
  }
});

test("admin can trigger resource billing settlement through the billing API", async () => {
  const calls = [];
  const appService = {
    async settleResourceBilling(input) {
      calls.push(["settleResourceBilling", input]);
      return {
        entries: [{ id: "ledger-1", type: "compute_debit" }],
        account: { id: input.accountId, balance: 248.8, frozen: 201.6 }
      };
    }
  };
  const { origin, close } = await listen(createRequestHandler({ appService }));
  try {
    const settled = await postJson(origin, "/api/billing/resource-settlements", {
      accountId: "pi-alpha",
      hours: 1,
      sourceEventId: "manual_resource_tick"
    });

    assert.equal(settled.response.status, 200);
    assert.deepEqual(settled.payload.entries.map((entry) => entry.type), ["compute_debit"]);
    assert.deepEqual(calls, [
      ["settleResourceBilling", {
        accountId: "pi-alpha",
        hours: 1,
        sourceEventId: "manual_resource_tick"
      }]
    ]);
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
