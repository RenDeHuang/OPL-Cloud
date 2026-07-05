import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { createAuthController, createRequestHandler } from "../../packages/console/api/server.js";
import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { emptyState, MemoryStore } from "../../packages/console/src/store.js";

const TEST_PRICING = {
  serverHourly: {
    basic: 1,
    pro: 4
  },
  diskGbMonth: 0.2,
  markup: 0.2
};

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

async function postJson(origin, path, body, headers = {}) {
  const response = await fetch(`${origin}${path}`, {
    method: "POST",
    headers: { "content-type": "application/json", ...headers },
    body: JSON.stringify(body)
  });
  return { response, payload: await response.json() };
}

function cookieFrom(response) {
  return response.headers.get("set-cookie")?.split(";")[0] || "";
}

test("auth controller does not bootstrap default users when no explicit seed exists", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-empty-bootstrap-"));
  try {
    const auth = createAuthController({
      env: {},
      usersPath: join(root, "users.json")
    });

    assert.deepEqual(await auth.listUsers(), []);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("default local admin credential cannot login without an explicit seed", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-empty-bootstrap-login-"));
  const appService = {
    async getState(accountId) {
      return { account: { id: accountId }, workspaces: [], packages: [], billingLedger: [], audit: [], notifications: [] };
    }
  };
  const auth = createAuthController({
    env: {},
    usersPath: join(root, "users.json")
  });
  const { origin, close } = await listen(createRequestHandler({ appService, auth }));
  try {
    const login = await postJson(origin, "/api/auth/login", {
      email: "admin@opl.local",
      password: "OplAdminPass2026!"
    });

    assert.equal(login.response.status, 401);
    assert.equal(login.payload.error, "invalid_credentials");
    assert.deepEqual(await auth.listUsers(), []);
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("auth controller can seed explicit environment users without built-in defaults", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-env-seed-"));
  try {
    const auth = createAuthController({
      env: {
        OPL_PI_EMAIL: "owner@example.com",
        OPL_PI_PASSWORD: "OwnerSecret2026!",
        OPL_PI_ACCOUNT_ID: "acct-owner",
        OPL_ADMIN_EMAIL: "admin@example.com",
        OPL_ADMIN_PASSWORD: "AdminSecret2026!",
        OPL_ADMIN_ACCOUNT_ID: "acct-admin"
      },
      usersPath: join(root, "users.json")
    });

    assert.deepEqual((await auth.listUsers()).map((user) => `${user.role}:${user.email}:${user.accountId}`), [
      "pi:owner@example.com:acct-owner",
      "admin:admin@example.com:acct-admin"
    ]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("commercial Console login creates a session and logout invalidates it", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-"));
  const appService = {
    async getState(accountId) {
      return { account: { id: accountId }, workspaces: [], packages: [], billingLedger: [], audit: [], notifications: [] };
    }
  };
  const auth = createAuthController({
    usersPath: join(root, "users.json"),
    seedUsers: [
      {
        id: "usr-pi",
        email: "pi@example.com",
        password: "secret-pi",
        name: "Lab Owner",
        role: "pi",
        accountId: "pi-alpha"
      }
    ]
  });
  const { origin, close } = await listen(createRequestHandler({ appService, auth }));
  try {
    const login = await postJson(origin, "/api/auth/login", {
      email: "pi@example.com",
      password: "secret-pi"
    });

    assert.equal(login.response.status, 200);
    assert.equal(login.payload.user.accountId, "pi-alpha");
    assert.equal(login.payload.user.email, "pi@example.com");
    assert.ok(login.payload.csrfToken);
    assert.match(login.response.headers.get("set-cookie"), /HttpOnly/);

    const cookie = cookieFrom(login.response);
    const meResponse = await fetch(`${origin}/api/auth/me`, { headers: { cookie } });
    const me = await meResponse.json();
    assert.equal(meResponse.status, 200);
    assert.equal(me.user.id, "usr-pi");

    const logout = await postJson(origin, "/api/auth/logout", {}, {
      cookie,
      "x-opl-csrf": login.payload.csrfToken
    });
    assert.equal(logout.response.status, 200);
    assert.match(logout.response.headers.get("set-cookie"), /Max-Age=0/);

    const afterLogout = await fetch(`${origin}/api/auth/me`, { headers: { cookie } });
    assert.equal(afterLogout.status, 401);
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("health check stays unauthenticated for Kubernetes probes", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-health-"));
  const appService = {
    async getState(accountId) {
      return { account: { id: accountId }, workspaces: [], packages: [], billingLedger: [], audit: [], notifications: [] };
    }
  };
  const auth = createAuthController({
    usersPath: join(root, "users.json"),
    seedUsers: [
      {
        id: "usr-pi",
        email: "pi@example.com",
        password: "secret-pi",
        name: "Lab Owner",
        role: "pi",
        accountId: "pi-alpha"
      }
    ]
  });
  const { origin, close } = await listen(createRequestHandler({ appService, auth }));
  try {
    const response = await fetch(`${origin}/api/healthz`);
    const payload = await response.json();
    assert.equal(response.status, 200);
    assert.deepEqual(payload, { ok: true, service: "opl-console" });
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("readiness checks stay unauthenticated for Kubernetes probes", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-readiness-"));
  const appService = {
    async runtimeReadiness() {
      return { provider: "tencent-tke", ready: true, missingEnv: [], missingTools: [] };
    },
    async productionReadiness() {
      return { ready: true, missingEnv: [], missingTools: [], failedChecks: [], checks: [] };
    }
  };
  const auth = createAuthController({
    usersPath: join(root, "users.json"),
    seedUsers: [
      {
        id: "usr-pi",
        email: "pi@example.com",
        password: "secret-pi",
        name: "Lab Owner",
        role: "pi",
        accountId: "pi-alpha"
      }
    ]
  });
  const { origin, close } = await listen(createRequestHandler({ appService, auth }));
  try {
    const runtime = await fetch(`${origin}/api/runtime/readiness`);
    assert.equal(runtime.status, 200);
    assert.deepEqual(await runtime.json(), { provider: "tencent-tke", ready: true, missingEnv: [], missingTools: [] });

    const production = await fetch(`${origin}/api/production/readiness`);
    assert.equal(production.status, 200);
    assert.deepEqual(await production.json(), { ready: true, missingEnv: [], missingTools: [], failedChecks: [], checks: [] });
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("PI sessions are account scoped and cannot top up balances", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-scope-"));
  const calls = [];
  const appService = {
    async getState(accountId) {
      calls.push(["getState", accountId]);
      return { account: { id: accountId }, workspaces: [], packages: [], billingLedger: [], audit: [], notifications: [] };
    },
    async manualTopUp(input) {
      calls.push(["manualTopUp", input]);
      return { id: input.accountId, balance: input.amount, frozen: 0 };
    }
  };
  const auth = createAuthController({
    usersPath: join(root, "users.json"),
    seedUsers: [
      {
        id: "usr-pi",
        email: "pi@example.com",
        password: "secret-pi",
        name: "Lab Owner",
        role: "pi",
        accountId: "pi-alpha"
      },
      {
        id: "usr-admin",
        email: "admin@example.com",
        password: "secret-admin",
        name: "Admin",
        role: "admin",
        accountId: "admin"
      }
    ]
  });
  const { origin, close } = await listen(createRequestHandler({ appService, auth }));
  try {
    const piLogin = await postJson(origin, "/api/auth/login", {
      email: "pi@example.com",
      password: "secret-pi"
    });
    const piCookie = cookieFrom(piLogin.response);

    const stateResponse = await fetch(`${origin}/api/state?accountId=pi-beta`, {
      headers: { cookie: piCookie }
    });
    const state = await stateResponse.json();
    assert.equal(stateResponse.status, 200);
    assert.equal(state.account.id, "pi-alpha");

    const piCredit = await postJson(origin, "/api/billing/topups", {
      accountId: "pi-alpha",
      amount: 200,
      reason: "manual_top_up"
    }, {
      cookie: piCookie,
      "x-opl-csrf": piLogin.payload.csrfToken
    });
    assert.equal(piCredit.response.status, 403);
    assert.equal(piCredit.payload.error, "admin_role_required");

    const adminLogin = await postJson(origin, "/api/auth/login", {
      email: "admin@example.com",
      password: "secret-admin"
    });
    const adminCredit = await postJson(origin, "/api/billing/topups", {
      accountId: "pi-alpha",
      amount: 200,
      reason: "manual_top_up"
    }, {
      cookie: cookieFrom(adminLogin.response),
      "x-opl-csrf": adminLogin.payload.csrfToken
    });
    assert.equal(adminCredit.response.status, 200);
    assert.deepEqual(calls, [
      ["getState", "pi-alpha"],
      ["manualTopUp", {
        accountId: "pi-alpha",
        amount: 200,
        reason: "manual_top_up",
        operatorUserId: "usr-admin",
        operatorAccountId: "admin"
      }]
    ]);
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("admin resource writes default to the admin account when no accountId is submitted", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-admin-resource-"));
  const calls = [];
  const appService = {
    async createComputeAllocation(input) {
      calls.push(["createComputeAllocation", input]);
      return { id: "compute-admin", ownerAccountId: input.accountId, status: "running" };
    },
    async createStorageVolume(input) {
      calls.push(["createStorageVolume", input]);
      return { id: "storage-admin", ownerAccountId: input.accountId, status: "available" };
    }
  };
  const auth = createAuthController({
    usersPath: join(root, "users.json"),
    seedUsers: [
      {
        id: "usr-admin",
        email: "admin@example.com",
        password: "secret-admin",
        name: "Admin",
        role: "admin",
        accountId: "admin"
      }
    ]
  });
  const { origin, close } = await listen(createRequestHandler({ appService, auth }));
  try {
    const adminLogin = await postJson(origin, "/api/auth/login", {
      email: "admin@example.com",
      password: "secret-admin"
    });
    const headers = {
      cookie: cookieFrom(adminLogin.response),
      "x-opl-csrf": adminLogin.payload.csrfToken
    };

    const compute = await postJson(origin, "/api/compute-allocations", {
      packageId: "basic",
      name: "Admin compute"
    }, headers);
    assert.equal(compute.response.status, 200);
    assert.equal(compute.payload.ownerAccountId, "admin");

    const storage = await postJson(origin, "/api/storage-volumes", {
      packageId: "basic",
      name: "Admin storage",
      sizeGb: 10
    }, headers);
    assert.equal(storage.response.status, 200);
    assert.equal(storage.payload.ownerAccountId, "admin");

    assert.deepEqual(calls, [
      ["createComputeAllocation", {
        accountId: "admin",
        userId: "usr-admin",
        packageId: "basic",
        name: "Admin compute"
      }],
      ["createStorageVolume", {
        accountId: "admin",
        userId: "usr-admin",
        packageId: "basic",
        name: "Admin storage",
        sizeGb: 10
      }]
    ]);
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("admin can invoke operator Workspace URL cleanup with CSRF", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-admin-cleanup-"));
  const calls = [];
  const appService = {
    async cleanupWorkspaceAccess(input) {
      calls.push(["cleanupWorkspaceAccess", input]);
      return {
        cleaned: [{ workspaceId: "ws-alpha", tokenStatus: "unavailable" }],
        skipped: [],
        activeResources: { compute: [], storage: [], attachments: [] }
      };
    }
  };
  const auth = createAuthController({
    usersPath: join(root, "users.json"),
    seedUsers: [
      {
        id: "usr-admin",
        email: "admin@example.com",
        password: "secret-admin",
        name: "Admin",
        role: "admin",
        accountId: "admin"
      }
    ]
  });
  const { origin, close } = await listen(createRequestHandler({ appService, auth }));
  try {
    const adminLogin = await postJson(origin, "/api/auth/login", {
      email: "admin@example.com",
      password: "secret-admin"
    });
    const cleanup = await postJson(origin, "/api/operator/cleanup-workspace-access", {
      accountId: "pi-alpha",
      workspaceIds: ["ws-alpha"],
      reason: "operator_cleanup_single"
    }, {
      cookie: cookieFrom(adminLogin.response),
      "x-opl-csrf": adminLogin.payload.csrfToken
    });

    assert.equal(cleanup.response.status, 200);
    assert.deepEqual(cleanup.payload.cleaned, [{ workspaceId: "ws-alpha", tokenStatus: "unavailable" }]);
    assert.deepEqual(calls, [
      ["cleanupWorkspaceAccess", {
        accountId: "pi-alpha",
        workspaceIds: ["ws-alpha"],
        reason: "operator_cleanup_single"
      }]
    ]);
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("admin can create a login user with an account wallet", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-create-user-"));
  const store = new MemoryStore();
  const appService = createOplCloud({
    store,
    runtimeProvider: { name: "test-provider" },
    pricing: TEST_PRICING
  });
  const auth = createAuthController({
    store,
    usersPath: join(root, "users.json"),
    seedUsers: [
      {
        id: "usr-admin",
        email: "admin@example.com",
        password: "secret-admin",
        name: "Admin",
        role: "admin",
        accountId: "admin",
        balance: 100
      }
    ]
  });
  const { origin, close } = await listen(createRequestHandler({ appService, auth }));
  try {
    const adminLogin = await postJson(origin, "/api/auth/login", {
      email: "admin@example.com",
      password: "secret-admin"
    });

    const created = await postJson(origin, "/api/users", {
      userId: "usr-owner",
      email: "owner@example.com",
      password: "OwnerPass2026!",
      name: "Owner",
      role: "pi",
      accountId: "acct-owner",
      initialBalance: 500
    }, {
      cookie: cookieFrom(adminLogin.response),
      "x-opl-csrf": adminLogin.payload.csrfToken
    });
    assert.equal(created.response.status, 200);
    assert.equal(created.payload.email, "owner@example.com");
    assert.equal(created.payload.accountId, "acct-owner");
    assert.equal(created.payload.passwordHash, undefined);

    const ownerLogin = await postJson(origin, "/api/auth/login", {
      email: "owner@example.com",
      password: "OwnerPass2026!"
    });
    assert.equal(ownerLogin.response.status, 200);
    assert.equal(ownerLogin.payload.user.accountId, "acct-owner");

    const ownerState = await fetch(`${origin}/api/state`, {
      headers: { cookie: cookieFrom(ownerLogin.response) }
    });
    const ownerPayload = await ownerState.json();
    assert.equal(ownerPayload.wallet.balance, 500);
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("operator token can create an admin session for production verifier actions", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-operator-"));
  const calls = [];
  const appService = {
    async manualTopUp(input) {
      calls.push(["manualTopUp", input]);
      return { id: input.accountId, balance: input.amount, frozen: 0 };
    }
  };
  const auth = createAuthController({
    env: { OPL_OPERATOR_SUMMARY_TOKEN: "operator-secret" },
    usersPath: join(root, "users.json"),
    seedUsers: [
      {
        id: "usr-admin",
        email: "admin@example.com",
        password: "secret-admin",
        name: "Admin",
        role: "admin",
        accountId: "admin"
      }
    ]
  });
  const { origin, close } = await listen(createRequestHandler({
    appService,
    auth,
    operatorSummaryToken: "operator-secret"
  }));
  try {
    const operatorLogin = await postJson(origin, "/api/auth/operator-login", {
      operatorToken: "operator-secret"
    });
    assert.equal(operatorLogin.response.status, 200);
    assert.equal(operatorLogin.payload.user.role, "admin");
    assert.ok(operatorLogin.payload.csrfToken);

    const operatorTopUp = await postJson(origin, "/api/billing/topups", {
      accountId: "pi-production-verifier",
      amount: 1000,
      reason: "production_verification_credit"
    }, {
      cookie: cookieFrom(operatorLogin.response),
      "x-opl-csrf-token": operatorLogin.payload.csrfToken
    });
    assert.equal(operatorTopUp.response.status, 200);
    assert.deepEqual(calls, [
      ["manualTopUp", {
        accountId: "pi-production-verifier",
        amount: 1000,
        reason: "production_verification_credit",
        operatorUserId: "usr-admin",
        operatorAccountId: "admin"
      }]
    ]);
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("admin compute allocation detail GET scopes to the requested account query", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-operator-compute-detail-"));
  const calls = [];
  const appService = {
    async computeAllocation(input) {
      calls.push(input);
      return {
        id: input.computeAllocationId,
        ownerAccountId: input.accountId,
        status: "provisioning"
      };
    }
  };
  const auth = createAuthController({
    env: { OPL_OPERATOR_SUMMARY_TOKEN: "operator-secret" },
    usersPath: join(root, "users.json"),
    seedUsers: [
      {
        id: "usr-admin",
        email: "admin@example.com",
        password: "secret-admin",
        name: "Admin",
        role: "admin",
        accountId: "admin"
      }
    ]
  });
  const { origin, close } = await listen(createRequestHandler({
    appService,
    auth,
    operatorSummaryToken: "operator-secret"
  }));
  try {
    const operatorLogin = await postJson(origin, "/api/auth/operator-login", {
      operatorToken: "operator-secret"
    });
    assert.equal(operatorLogin.response.status, 200);

    const response = await fetch(`${origin}/api/compute-allocations/compute-prod001?accountId=pi-production-verifier`, {
      headers: {
        cookie: cookieFrom(operatorLogin.response)
      }
    });
    const payload = await response.json();

    assert.equal(response.status, 200);
    assert.equal(payload.ownerAccountId, "pi-production-verifier");
    assert.deepEqual(calls, [
      {
        accountId: "pi-production-verifier",
        computeAllocationId: "compute-prod001",
        userId: "usr-admin"
      }
    ]);
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("auth users seed into the control-plane store and survive controller recreation", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-store-auth-"));
  const store = new MemoryStore();
  const appService = {
    async getState(accountId) {
      return { account: { id: accountId }, workspaces: [], packages: [], billingLedger: [], audit: [], notifications: [] };
    }
  };
  const seedUsers = [
    {
      id: "usr-pi-seed",
      email: "seed-owner@example.com",
      password: "secret-seed",
      name: "Seeded Lab Owner",
      role: "pi",
      accountId: "pi-seed"
    }
  ];
  const usersPath = join(root, "users.json");

  try {
    const firstAuth = createAuthController({ store, usersPath, seedUsers });
    const firstServer = await listen(createRequestHandler({ appService, auth: firstAuth }));
    try {
      const login = await postJson(firstServer.origin, "/api/auth/login", {
        email: "seed-owner@example.com",
        password: "secret-seed"
      });
      assert.equal(login.response.status, 200);
      assert.equal(login.payload.user.accountId, "pi-seed");
    } finally {
      await firstServer.close();
    }

    const persisted = await store.read();
    assert.equal(persisted.users["usr-pi-seed"].email, "seed-owner@example.com");
    assert.equal(persisted.users["usr-pi-seed"].accountId, "pi-seed");
    assert.equal(persisted.users["usr-pi-seed"].role, "pi");
    assert.equal(persisted.users["usr-pi-seed"].status, "active");
    assert.match(persisted.users["usr-pi-seed"].passwordHash, /^scrypt:/);
    assert.equal(persisted.users["usr-pi-seed"].password, undefined);
    assert.equal(persisted.users["usr-pi-seed"].balance, 0);
    assert.equal(persisted.users["usr-pi-seed"].frozen, 0);
    assert.deepEqual(persisted.users["usr-pi-seed"].holds, {});
    assert.equal(persisted.users["usr-pi-seed"].totalRecharged, 0);

    const recreatedAuth = createAuthController({ store, usersPath, seedUsers: [] });
    const recreatedServer = await listen(createRequestHandler({ appService, auth: recreatedAuth }));
    try {
      const login = await postJson(recreatedServer.origin, "/api/auth/login", {
        email: "seed-owner@example.com",
        password: "secret-seed"
      });
      assert.equal(login.response.status, 200);
      assert.equal(login.payload.user.id, "usr-pi-seed");
    } finally {
      await recreatedServer.close();
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("store-backed disabled users cannot login and existing sessions are revoked", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-store-auth-disabled-"));
  const store = new MemoryStore();
  const appService = {
    async getState(accountId) {
      return { account: { id: accountId }, workspaces: [], packages: [], billingLedger: [], audit: [], notifications: [] };
    }
  };
  const auth = createAuthController({
    store,
    usersPath: join(root, "users.json"),
    seedUsers: [
      {
        id: "usr-pi-disabled",
        email: "disabled@example.com",
        password: "secret-disabled",
        name: "Disabled PI",
        role: "pi",
        accountId: "pi-disabled"
      }
    ]
  });
  const { origin, close } = await listen(createRequestHandler({ appService, auth }));
  try {
    const login = await postJson(origin, "/api/auth/login", {
      email: "disabled@example.com",
      password: "secret-disabled"
    });
    assert.equal(login.response.status, 200);
    const cookie = cookieFrom(login.response);

    await store.update((state) => {
      state.users["usr-pi-disabled"].status = "disabled";
      state.users["usr-pi-disabled"].updatedAt = "2026-07-02T00:00:00.000Z";
    });

    const existingSession = await fetch(`${origin}/api/auth/me`, { headers: { cookie } });
    assert.equal(existingSession.status, 401);

    const blockedLogin = await postJson(origin, "/api/auth/login", {
      email: "disabled@example.com",
      password: "secret-disabled"
    });
    assert.equal(blockedLogin.response.status, 401);
    assert.equal(blockedLogin.payload.error, "invalid_credentials");
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("store-backed auth ignores file-backed usersPath data and seeds current store users", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-store-auth-current-seed-"));
  const usersPath = join(root, "users.json");
  const store = new MemoryStore();
  const appService = {
    async getState(accountId) {
      return { account: { id: accountId }, workspaces: [], packages: [], billingLedger: [], audit: [], notifications: [] };
    }
  };
  await writeFile(usersPath, JSON.stringify({
    users: [
      {
        id: "usr-file",
        email: "file@example.com",
        password: "file-secret",
        name: "File PI",
        role: "pi",
        accountId: "file-account"
      }
    ]
  }));

  const auth = createAuthController({
    store,
    usersPath,
    seedUsers: [
      {
        id: "usr-seed",
        email: "seed@example.com",
        password: "seed-secret",
        name: "Seed PI",
        role: "pi",
        accountId: "seed-account"
      }
    ]
  });
  const { origin, close } = await listen(createRequestHandler({ appService, auth }));
  try {
    const fileLogin = await postJson(origin, "/api/auth/login", {
      email: "file@example.com",
      password: "file-secret"
    });
    assert.equal(fileLogin.response.status, 401);

    const seedLogin = await postJson(origin, "/api/auth/login", {
      email: "seed@example.com",
      password: "seed-secret"
    });
    assert.equal(seedLogin.response.status, 200);
    assert.equal(seedLogin.payload.user.accountId, "seed-account");

    const persisted = await store.read();
    assert.equal(persisted.users["usr-file"], undefined);
    assert.equal(persisted.users["usr-seed"].email, "seed@example.com");
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("store-backed auth does not create account wallet mirrors for persisted login users", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-store-auth-no-account-mirror-"));
  const usersPath = join(root, "users.json");
  const store = new MemoryStore();
  const appService = {
    async getState(accountId) {
      return { account: { id: accountId }, workspaces: [], packages: [], billingLedger: [], audit: [], notifications: [] };
    }
  };
  const seedUsers = [
    {
      id: "usr-pi-repair",
      email: "repair@example.com",
      password: "repair-secret",
      name: "Repair PI",
      role: "pi",
      accountId: "pi-repair"
    }
  ];

  const firstAuth = createAuthController({ store, usersPath, seedUsers });
  const firstServer = await listen(createRequestHandler({ appService, auth: firstAuth }));
  try {
    const seeded = await postJson(firstServer.origin, "/api/auth/login", {
      email: "repair@example.com",
      password: "repair-secret"
    });
    assert.equal(seeded.response.status, 200);
  } finally {
    await firstServer.close();
  }

  const currentAuth = createAuthController({ store, usersPath, seedUsers: [] });
  const currentServer = await listen(createRequestHandler({ appService, auth: currentAuth }));
  try {
    const login = await postJson(currentServer.origin, "/api/auth/login", {
      email: "repair@example.com",
      password: "repair-secret"
    });
    assert.equal(login.response.status, 200);

    const persisted = await store.read();
    assert.equal(persisted.users["usr-pi-repair"].accountId, "pi-repair");
  } finally {
    await currentServer.close();
    await rm(root, { recursive: true, force: true });
  }
});

test("store-backed auth does not rewrite already repaired wallet users", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-store-auth-no-rewrite-"));
  const baseStore = new MemoryStore({
    ...emptyState(),
    users: {
      "usr-pi-stable": {
        id: "usr-pi-stable",
        email: "stable@example.com",
        name: "Stable PI",
        role: "pi",
        accountId: "pi-stable",
        status: "active",
        passwordHash: "scrypt:salt:hash",
        balance: 25,
        frozen: 0,
        holds: {},
        totalRecharged: 25
      }
    }
  });
  let updateCalls = 0;
  const store = {
    read: () => baseStore.read(),
    update: async (mutator) => {
      updateCalls += 1;
      return baseStore.update(mutator);
    }
  };

  try {
    const auth = createAuthController({ store, usersPath: join(root, "users.json"), seedUsers: [] });
    const users = await auth.listUsers();

    assert.equal(users.length, 1);
    assert.equal(users[0].email, "stable@example.com");
    assert.equal(updateCalls, 0);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
