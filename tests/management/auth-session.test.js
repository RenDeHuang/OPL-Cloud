import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { createAuthController, createRequestHandler } from "../../packages/console/api/server.js";
import { emptyState, MemoryStore } from "../../packages/console/src/store.js";

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

test("auth controller bootstraps only the fixed admin when no explicit seed exists", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-admin-bootstrap-"));
  try {
    const auth = createAuthController({
      env: {},
      usersPath: join(root, "users.json")
    });

    assert.deepEqual((await auth.listUsers()).map((user) => `${user.role}:${user.email}:${user.accountId}`), [
      "admin:admin@opl.local:admin"
    ]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("built-in admin can login but does not create a demo Lab Owner", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-auth-admin-bootstrap-login-"));
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

    assert.equal(login.response.status, 200);
    assert.equal(login.payload.user.role, "admin");
    assert.equal(login.payload.user.accountId, "admin");
    assert.deepEqual((await auth.listUsers()).map((user) => user.role), ["admin"]);
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
    assert.deepEqual(persisted.accounts, {});

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

test("store-backed auth ignores old usersPath data and seeds current store users", async () => {
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
        id: "usr-legacy",
        email: "legacy@example.com",
        password: "legacy-secret",
        name: "Legacy PI",
        role: "pi",
        accountId: "legacy-account"
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
    const legacyLogin = await postJson(origin, "/api/auth/login", {
      email: "legacy@example.com",
      password: "legacy-secret"
    });
    assert.equal(legacyLogin.response.status, 401);

    const seedLogin = await postJson(origin, "/api/auth/login", {
      email: "seed@example.com",
      password: "seed-secret"
    });
    assert.equal(seedLogin.response.status, 200);
    assert.equal(seedLogin.payload.user.accountId, "seed-account");

    const persisted = await store.read();
    assert.equal(persisted.users["usr-legacy"], undefined);
    assert.equal(persisted.users["usr-seed"].email, "seed@example.com");
    assert.deepEqual(persisted.accounts, {});
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
    assert.deepEqual(persisted.accounts, {});
  } finally {
    await currentServer.close();
    await rm(root, { recursive: true, force: true });
  }
});

test("store-backed auth migrates legacy account wallet fields onto persisted users", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-store-auth-wallet-"));
  const usersPath = join(root, "users.json");
  const store = new MemoryStore({
    accounts: {
      "pi-wallet": {
        id: "pi-wallet",
        balance: 248.7967,
        frozen: 202.16,
        holds: {
          compute: 201.6,
          storage: 0.56
        },
        totalRecharged: 250
      }
    },
    organizations: {},
    users: {
      "usr-pi-wallet": {
        id: "usr-pi-wallet",
        email: "wallet@example.com",
        name: "Wallet PI",
        role: "pi",
        accountId: "pi-wallet",
        status: "active",
        passwordHash: "scrypt:94ffcb9fc02bdc377ead1ae046cfe792:71389fc96c628b6779c107f28bbdce446a6b82f1a4d6cd1a3643801fff81d52e95a1271b02b999f56ae5f63072b0a5609a893fdfb7cf106b9a18bed169391167"
      }
    },
    memberships: [],
    workspaces: {},
    storageBackups: [],
    billingReconciliationReports: [],
    evidenceLedger: [],
    billingLedger: [],
    audit: [],
    notifications: [],
    runtimeOperations: [],
    resourceUsageLogs: [],
    requestUsageLogs: []
  });
  try {
    const auth = createAuthController({ store, usersPath, seedUsers: [] });
    await auth.listUsers();

    const persisted = await store.read();
    assert.equal(persisted.users["usr-pi-wallet"].balance, 248.7967);
    assert.equal(persisted.users["usr-pi-wallet"].frozen, 202.16);
    assert.deepEqual(persisted.users["usr-pi-wallet"].holds, {
      compute: 201.6,
      storage: 0.56
    });
    assert.equal(persisted.users["usr-pi-wallet"].totalRecharged, 250);
    assert.deepEqual(persisted.accounts, {});
  } finally {
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
