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
        name: "Pilot PI",
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
        name: "Pilot PI",
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
    async creditAccount(input) {
      calls.push(["creditAccount", input]);
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
        name: "Pilot PI",
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

    const piCredit = await postJson(origin, "/api/accounts/credit", {
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
    const adminCredit = await postJson(origin, "/api/accounts/credit", {
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
      ["creditAccount", { accountId: "pi-alpha", amount: 200, reason: "manual_top_up" }]
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
      id: "usr-pi-demo",
      email: "pi-demo@example.com",
      password: "secret-demo",
      name: "Demo PI",
      role: "pi",
      accountId: "pi-demo"
    }
  ];
  const usersPath = join(root, "users.json");

  try {
    const firstAuth = createAuthController({ store, usersPath, seedUsers });
    const firstServer = await listen(createRequestHandler({ appService, auth: firstAuth }));
    try {
      const login = await postJson(firstServer.origin, "/api/auth/login", {
        email: "pi-demo@example.com",
        password: "secret-demo"
      });
      assert.equal(login.response.status, 200);
      assert.equal(login.payload.user.accountId, "pi-demo");
    } finally {
      await firstServer.close();
    }

    const persisted = await store.read();
    assert.equal(persisted.users["usr-pi-demo"].email, "pi-demo@example.com");
    assert.equal(persisted.users["usr-pi-demo"].accountId, "pi-demo");
    assert.equal(persisted.users["usr-pi-demo"].role, "pi");
    assert.equal(persisted.users["usr-pi-demo"].status, "active");
    assert.match(persisted.users["usr-pi-demo"].passwordHash, /^scrypt:/);
    assert.equal(persisted.users["usr-pi-demo"].password, undefined);
    assert.deepEqual(persisted.accounts["pi-demo"], {
      id: "pi-demo",
      balance: 0,
      frozen: 0,
      holds: {}
    });

    const recreatedAuth = createAuthController({ store, usersPath, seedUsers: [] });
    const recreatedServer = await listen(createRequestHandler({ appService, auth: recreatedAuth }));
    try {
      const login = await postJson(recreatedServer.origin, "/api/auth/login", {
        email: "pi-demo@example.com",
        password: "secret-demo"
      });
      assert.equal(login.response.status, 200);
      assert.equal(login.payload.user.id, "usr-pi-demo");
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

test("store-backed auth imports legacy JSON users before bootstrap seeds", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-store-auth-legacy-"));
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
    assert.equal(legacyLogin.response.status, 200);
    assert.equal(legacyLogin.payload.user.accountId, "legacy-account");

    const seedLogin = await postJson(origin, "/api/auth/login", {
      email: "seed@example.com",
      password: "seed-secret"
    });
    assert.equal(seedLogin.response.status, 401);

    const persisted = await store.read();
    assert.equal(persisted.users["usr-legacy"].email, "legacy@example.com");
    assert.equal(persisted.users["usr-seed"], undefined);
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("store-backed auth repairs missing account records for persisted login users", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-store-auth-account-"));
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

  await store.update((state) => {
    delete state.accounts["pi-repair"];
  });

  const repairedAuth = createAuthController({ store, usersPath, seedUsers: [] });
  const repairedServer = await listen(createRequestHandler({ appService, auth: repairedAuth }));
  try {
    const login = await postJson(repairedServer.origin, "/api/auth/login", {
      email: "repair@example.com",
      password: "repair-secret"
    });
    assert.equal(login.response.status, 200);

    const persisted = await store.read();
    assert.deepEqual(persisted.accounts["pi-repair"], {
      id: "pi-repair",
      balance: 0,
      frozen: 0,
      holds: {}
    });
  } finally {
    await repairedServer.close();
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
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("store-backed auth does not rewrite already repaired wallet users", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-store-auth-no-rewrite-"));
  const baseStore = new MemoryStore({
    ...emptyState(),
    accounts: {
      "pi-stable": {
        id: "pi-stable",
        balance: 25,
        frozen: 0,
        holds: {}
      }
    },
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
