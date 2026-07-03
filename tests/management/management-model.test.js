import assert from "node:assert/strict";
import test from "node:test";

import {
  createCurrentTestService,
  provisionWorkspace
} from "../helpers/current-resource-chain.js";

test("Console management model links users, organizations, memberships, billing account, packages, resources, and Workspace URL entries", async () => {
  const service = createCurrentTestService();

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
    amount: 300,
    reason: "org_top_up"
  });

  const { workspace } = await provisionWorkspace(service, {
    accountId: organization.billingAccountId,
    organizationId: organization.id,
    userId: user.id,
    workspaceName: "Managed Lab",
    packageId: "basic"
  });

  assert.deepEqual(workspace.owner, {
    type: "organization",
    organizationId: "org-lab",
    userId: "usr-ada",
    billingAccountId: "org-lab"
  });
  assert.equal(workspace.ownerAccountId, "org-lab");
  assert.equal(workspace.packageId, "basic");
  assert.ok(workspace.computeId);
  assert.ok(workspace.storageId);
  assert.ok(workspace.attachmentId);

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
  assert.equal(management.billingAccount.balance > 0, true);
  assert.equal(management.billingAccount.frozen > 0, true);
  assert.deepEqual(management.packages.map((plan) => plan.id), ["basic", "pro"]);
  assert.deepEqual(management.workspaces.map((item) => item.id), [workspace.id]);
});

test("admin management state lists every login user and account wallet without organization scope", async () => {
  const service = createCurrentTestService();

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

test("organization Workspace creation fails closed unless the user is an active organization member", async () => {
  const service = createCurrentTestService();
  await service.createOrganization({ organizationId: "org-lab", name: "OPL Lab" });
  await service.createUser({ userId: "usr-ada", email: "ada@example.com" });
  await service.manualTopUp({ accountId: "org-lab", amount: 300, reason: "org_top_up" });

  const compute = await service.createComputeResource({
    accountId: "org-lab",
    userId: "usr-ada",
    packageId: "basic",
    name: "Blocked compute"
  });
  const storage = await service.createStorageVolume({
    accountId: "org-lab",
    userId: "usr-ada",
    packageId: "basic",
    sizeGb: 10,
    name: "Blocked storage"
  });
  const attachment = await service.attachStorage({
    accountId: "org-lab",
    computeId: compute.id,
    storageId: storage.id,
    mountPath: "/data"
  });

  await assert.rejects(
    service.createWorkspace({
      accountId: "org-lab",
      organizationId: "org-lab",
      userId: "usr-ada",
      workspaceName: "Blocked Lab",
      attachmentId: attachment.id
    }),
    /organization_membership_required/
  );
});

test("support tickets are account-scoped durable Console objects", async () => {
  const service = createCurrentTestService();
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
    category: "workspace",
    priority: "high",
    workspaceId: "ws-alpha",
    description: "URL returns 403"
  });

  assert.equal(ticket.status, "open");
  assert.equal(ticket.messages[0].text, "URL returns 403");

  await assert.rejects(
    service.createSupportTicket({
      accountId: "pi-alpha",
      userId: "usr-pi-alpha",
      title: "Wrong Workspace",
      category: "workspace",
      workspaceId: "ws-beta",
      message: "Should fail"
    }),
    /workspace_not_found/
  );

  const state = await service.getState("pi-alpha");
  assert.deepEqual(state.supportTickets.map((item) => item.id), [ticket.id]);
});
