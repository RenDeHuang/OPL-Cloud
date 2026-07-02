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
