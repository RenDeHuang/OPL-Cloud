import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
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

async function createWorkspaceEntry(service, {
  accountId = "",
  organizationId = "",
  userId = "",
  workspaceName,
  packageId = "basic"
}) {
  const ownerAccountId = accountId || organizationId;
  const storage = await service.createStorageVolume({ accountId: ownerAccountId, packageId, name: `${workspaceName} storage` });
  const compute = await service.createComputeAllocation({ accountId: ownerAccountId, packageId, name: `${workspaceName} compute` });
  const attachment = await service.attachStorage({
    accountId: ownerAccountId,
    computeAllocationId: compute.id,
    storageId: storage.id,
    mountPath: "/data"
  });
  return service.createWorkspace({
    accountId,
    organizationId,
    userId,
    workspaceName,
    attachmentId: attachment.id
  });
}

test("Console management model links users, organizations, memberships, billing account, packages, and holds", async () => {
  const service = createTestService();

  const organization = await service.createOrganization({
    organizationId: "org-lab",
    name: "OPL Lab"
  });
  const user = await service.createUser({
    userId: "usr-ada",
    email: "ada@example.com",
    name: "Ada"
  });
  const membership = await service.addOrganizationMember({
    organizationId: organization.id,
    userId: user.id,
    role: "owner"
  });

  assert.equal(organization.billingAccountId, "org-lab");
  assert.equal(membership.status, "active");

  await service.manualTopUp({
    accountId: organization.billingAccountId,
    amount: 250,
    reason: "org_top_up"
  });

  const workspace = await createWorkspaceEntry(service, {
    organizationId: organization.id,
    userId: user.id,
    workspaceName: "Managed Lab"
  });

  assert.deepEqual(workspace.owner, {
    type: "organization",
    organizationId: "org-lab",
    userId: "usr-ada",
    billingAccountId: "org-lab"
  });
  assert.equal(workspace.ownerAccountId, "org-lab");
  assert.equal(workspace.packageId, "basic");

  const management = await service.managementState({ organizationId: "org-lab" });
  assert.deepEqual(management.organization, organization);
  assert.deepEqual(management.users.map((item) => item.id), ["usr-ada"]);
  assert.deepEqual(management.memberships.map((item) => ({
    organizationId: item.organizationId,
    userId: item.userId,
    role: item.role,
    status: item.status
  })), [
    {
      organizationId: "org-lab",
      userId: "usr-ada",
      role: "owner",
      status: "active"
    }
  ]);
  assert.equal(management.billingAccount.id, "org-lab");
  assert.equal(management.billingAccount.balance, 250);
  assert.equal(management.billingAccount.frozen, 202.16);
  assert.deepEqual(management.packages.map((plan) => plan.id), ["basic", "pro"]);
  assert.deepEqual(management.workspaces.map((item) => item.id), [workspace.id]);
});

test("admin management state lists every login user and account wallet without organization scope", async () => {
  const service = createTestService();

  await service.store.update((state) => {
    state.users["usr-admin"] = {
      id: "usr-admin",
      email: "admin@example.com",
      name: "Admin",
      role: "admin",
      accountId: "admin",
      status: "active",
      balance: 100,
      frozen: 0,
      holds: {},
      totalRecharged: 100,
      passwordHash: "scrypt:redacted"
    };
    state.users["usr-owner"] = {
      id: "usr-owner",
      email: "owner@example.com",
      name: "Owner",
      role: "pi",
      accountId: "acct-owner",
      status: "active",
      balance: 500,
      frozen: 20,
      holds: { compute: 20 },
      totalRecharged: 500,
      passwordHash: "scrypt:redacted"
    };
  });

  const management = await service.managementState({});

  assert.equal(management.organization, null);
  assert.deepEqual(management.users.map((user) => ({
    id: user.id,
    email: user.email,
    role: user.role,
    accountId: user.accountId,
    passwordHash: user.passwordHash
  })), [
    {
      id: "usr-admin",
      email: "admin@example.com",
      role: "admin",
      accountId: "admin",
      passwordHash: undefined
    },
    {
      id: "usr-owner",
      email: "owner@example.com",
      role: "pi",
      accountId: "acct-owner",
      passwordHash: undefined
    }
  ]);
  assert.deepEqual(management.accounts.map((account) => ({
    id: account.id,
    balance: account.balance,
    frozen: account.frozen
  })), [
    { id: "admin", balance: 100, frozen: 0 },
    { id: "acct-owner", balance: 500, frozen: 20 }
  ]);
});

test("commercial read models do not expose raw providerData", async () => {
  const service = createTestService();

  await service.store.update((state) => {
    state.users["usr-owner"] = {
      id: "usr-owner",
      email: "owner@example.com",
      name: "Owner",
      role: "pi",
      accountId: "acct-owner",
      status: "active",
      balance: 500,
      frozen: 0,
      holds: {},
      totalRecharged: 500,
      passwordHash: "scrypt:redacted"
    };
    state.computeAllocations.push({
      id: "compute-sensitive",
      ownerAccountId: "acct-owner",
      status: "failed",
      providerResourceId: "np-sensitive",
      providerRequestId: "req-safe",
      safeMessage: "CAM denied ScaleNodePool",
      providerData: {
        action: "create_compute_allocation",
        rawTencentResponse: { secretShape: "must-not-leak" }
      }
    });
    state.storageVolumes.push({
      id: "storage-sensitive",
      ownerAccountId: "acct-owner",
      status: "available",
      providerResourceId: "pvc-sensitive",
      providerData: {
        rawTencentResponse: { secretShape: "must-not-leak" }
      }
    });
    state.storageAttachments.push({
      id: "attach-sensitive",
      ownerAccountId: "acct-owner",
      status: "attached",
      computeAllocationId: "compute-sensitive",
      storageId: "storage-sensitive",
      providerData: {
        rawTencentResponse: { secretShape: "must-not-leak" }
      }
    });
  });

  const ownerState = await service.getState("acct-owner");
  const management = await service.managementState({});

  for (const collection of [
    ownerState.computeAllocations,
    ownerState.storageVolumes,
    ownerState.storageAttachments,
    management.computeAllocations,
    management.storageVolumes,
    management.storageAttachments
  ]) {
    assert.equal(collection[0].providerData, undefined);
  }
  assert.equal(ownerState.computeAllocations[0].providerRequestId, "req-safe");
  assert.equal(ownerState.computeAllocations[0].safeMessage, "CAM denied ScaleNodePool");
});

test("admin can disable and delete login users while preserving account resources and billing evidence", async () => {
  const service = createTestService();

  await service.createUser({
    userId: "usr-owner",
    email: "owner@example.com",
    name: "Owner",
    role: "pi",
    accountId: "acct-owner",
    password: "OwnerPass2026!",
    initialBalance: 500
  });
  const workspace = await createWorkspaceEntry(service, {
    accountId: "acct-owner",
    workspaceName: "Retained Lab"
  });

  const disabled = await service.disableUser({
    userId: "usr-owner",
    operatorUserId: "usr-admin",
    reason: "security_review"
  });
  assert.equal(disabled.status, "disabled");
  assert.equal(disabled.accountId, "acct-owner");

  let management = await service.managementState({});
  assert.equal(management.users.find((user) => user.id === "usr-owner").status, "disabled");
  assert.equal(management.accounts.find((account) => account.id === "acct-owner").balance, 500);
  assert.ok(management.workspaces.find((item) => item.id === workspace.id), "disabled user resources must stay visible to admin");

  const deleted = await service.deleteUser({
    userId: "usr-owner",
    operatorUserId: "usr-admin",
    reason: "account_closed"
  });
  assert.equal(deleted.status, "deleted");
  assert.equal(deleted.accountId, "acct-owner");

  management = await service.managementState({});
  assert.equal(management.users.find((user) => user.id === "usr-owner").status, "deleted");
  assert.equal(management.accounts.find((account) => account.id === "acct-owner").balance, 500);
  assert.ok(management.workspaces.find((item) => item.id === workspace.id), "deleted login user must not delete Workspace ownership evidence");

  const auditTypes = (await service.store.read()).audit.map((event) => event.type);
  assert.ok(auditTypes.includes("user.disabled"));
  assert.ok(auditTypes.includes("user.deleted"));
});

test("organization Workspace creation fails closed unless the user is an active organization member", async () => {
  const service = createTestService();
  await service.createOrganization({ organizationId: "org-lab", name: "OPL Lab" });
  await service.createUser({ userId: "usr-ada", email: "ada@example.com" });
  await service.manualTopUp({ accountId: "org-lab", amount: 250, reason: "org_top_up" });

  await assert.rejects(
    createWorkspaceEntry(service, {
      organizationId: "org-lab",
      userId: "usr-ada",
      workspaceName: "Blocked Lab"
    }),
    /organization_membership_required/
  );
});

test("support tickets are account-scoped durable Console objects", async () => {
  const service = createTestService();
  await service.store.update((state) => {
    state.workspaces["ws-alpha"] = {
      id: "ws-alpha",
      ownerAccountId: "pi-alpha",
      name: "Workspace URL Lab"
    };
    state.workspaces["ws-beta"] = {
      id: "ws-beta",
      ownerAccountId: "pi-beta",
      name: "Other Lab"
    };
  });

  const ticket = await service.createSupportTicket({
    accountId: "pi-alpha",
    userId: "usr-pi-alpha",
    title: "Workspace URL",
    category: "Workspace",
    priority: "high",
    workspaceId: "ws-alpha",
    description: "Workspace URL returns 403.",
    author: "pi@example.com"
  });

  assert.equal(ticket.accountId, "pi-alpha");
  assert.equal(ticket.status, "open");
  assert.equal(ticket.messages[0].author, "pi@example.com");

  assert.deepEqual((await service.supportTickets({ accountId: "pi-alpha" })).map((item) => item.id), [ticket.id]);
  assert.deepEqual(await service.supportTickets({ accountId: "pi-beta" }), []);

  const state = await service.store.read();
  assert.equal(state.supportTickets[0].id, ticket.id);
  assert.equal(state.audit.at(-1).type, "support.ticket_created");

  await assert.rejects(
    service.createSupportTicket({
      accountId: "pi-alpha",
      userId: "usr-pi-alpha",
      title: "Wrong Workspace",
      workspaceId: "ws-beta"
    }),
    /support_ticket_workspace_not_in_account/
  );
});

test("operator summary includes safe production E2E records derived from existing ledger evidence", async () => {
  const service = createTestService();
  await service.store.update((state) => {
    state.users["usr-owner"] = {
      id: "usr-owner",
      email: "owner@example.com",
      accountId: "pi-production-verifier",
      status: "active",
      balance: 1000,
      frozen: 0,
      holds: {},
      totalRecharged: 1000
    };
    state.billingLedger.push({
      id: "ledger-credit",
      accountId: "pi-production-verifier",
      workspaceId: "account",
      type: "credit",
      amount: 1000,
      sourceEventId: "production_verification_credit:run-123",
      createdAt: "2026-07-05T00:00:00.000Z"
    });
    state.billingLedger.push({
      id: "ledger-request",
      accountId: "pi-production-verifier",
      workspaceId: "ws-prod",
      type: "request_debit",
      amount: -0.01,
      sourceEventId: "production_verification_request_usage:run-123",
      createdAt: "2026-07-05T00:05:00.000Z"
    });
    state.runtimeOperations.push({
      id: "op-create",
      accountId: "pi-production-verifier",
      workspaceId: "ws-prod",
      resourceId: "compute-prod",
      operationType: "create_compute_allocation",
      status: "completed",
      updatedAt: "2026-07-05T00:03:00.000Z"
    });
  });

  const summary = await service.operatorSummary({});

  assert.equal(summary.productionE2E.total, 1);
  assert.deepEqual(summary.productionE2E.recent, [
    {
      runId: "run-123",
      accountId: "pi-production-verifier",
      workspaceId: "ws-prod",
      status: "passed",
      checks: ["credit", "request_usage", "runtime_operation"],
      lastSeenAt: "2026-07-05T00:05:00.000Z"
    }
  ]);
  assert.equal(JSON.stringify(summary.productionE2E).includes("token"), false, "E2E summary must not expose URL tokens or secrets");
});
