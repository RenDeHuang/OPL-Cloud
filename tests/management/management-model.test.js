import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
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
      }
    },
    pricing: TEST_PRICING
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

  const workspace = await service.createWorkspace({
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
  assert.equal(management.billingAccount.balance, 248.7967);
  assert.equal(management.billingAccount.frozen, 202.16);
  assert.deepEqual(management.packages.map((plan) => plan.id), ["basic", "pro"]);
  assert.deepEqual(management.workspaces.map((item) => item.id), [workspace.id]);
});

test("organization Workspace creation fails closed unless the user is an active organization member", async () => {
  const service = createTestService();
  await service.createOrganization({ organizationId: "org-lab", name: "OPL Lab" });
  await service.createUser({ userId: "usr-ada", email: "ada@example.com" });
  await service.manualTopUp({ accountId: "org-lab", amount: 250, reason: "org_top_up" });

  await assert.rejects(
    service.createWorkspace({
      organizationId: "org-lab",
      userId: "usr-ada",
      workspaceName: "Blocked Lab",
      packageId: "basic"
    }),
    /organization_membership_required/
  );
});
