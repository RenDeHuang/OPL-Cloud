import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { createRequestHandler } from "../../services/api/server.js";
import { createOplCloud } from "../../services/api/src/opl-cloud.js";
import { LocalDockerProvider } from "../../services/api/src/runtime-providers/local-docker.js";
import { MemoryStore } from "../../services/api/src/store.js";

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

test("static app routes fall back to index.html for commercial Home and Login routes", async () => {
  const staticDir = await mkdtemp(join(tmpdir(), "opl-cloud-static-"));
  const indexHtml = "<!doctype html><title>OPL Console</title><div id=\"root\"></div>";
  await writeFile(join(staticDir, "index.html"), indexHtml);

  const { origin, close } = await listen(createRequestHandler({ staticDir }));
  try {
    const homeResponse = await fetch(`${origin}/`);
    const loginResponse = await fetch(`${origin}/login`);
    const assetResponse = await fetch(`${origin}/assets/missing.js`);

    assert.equal(homeResponse.status, 200);
    assert.equal(await homeResponse.text(), indexHtml);
    assert.equal(loginResponse.status, 200);
    assert.equal(await loginResponse.text(), indexHtml);
    assert.equal(assetResponse.status, 404);
  } finally {
    await close();
    await rm(staticDir, { recursive: true, force: true });
  }
});

test("workspace URL route validates token and returns OPL Workspace entry page", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-route-"));
  const appService = createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: new LocalDockerProvider({
      rootDir: root,
      baseUrl: "http://127.0.0.1:8787",
      execute: false
    }),
    pricing: {
      serverHourly: { basic: 1, pro: 4 },
      diskGbMonth: 0.2,
      markup: 0.2
    }
  });
  const { origin, close } = await listen(createRequestHandler({
    appService,
    enforceSessionScope: false,
    enforceCsrf: false
  }));
  try {
    await appService.creditAccount({ accountId: "pi-route", amount: 250, reason: "route_test_credit" });
    const workspace = await appService.createWorkspace({
      accountId: "pi-route",
      workspaceName: "Route Lab",
      packageId: "basic"
    });

    const invalidResponse = await fetch(`${origin}/workspaces/${workspace.slug}?token=wrong`);
    assert.equal(invalidResponse.status, 403);

    const validResponse = await fetch(`${origin}/workspaces/${workspace.slug}?token=${workspace.access.token}`);
    const html = await validResponse.text();
    assert.equal(validResponse.status, 200);
    assert.match(html, /Route Lab/);
    assert.match(html, /OPL Workspace/);
    assert.match(html, /Workspace link is valid/);
    assert.doesNotMatch(html, /docker-compose|runtime target|Docker|mountPath|\.runtime/);
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("runtime readiness route reports provider execution gaps without creating resources", async () => {
  const appService = {
    runtimeReadiness: async () => ({
      provider: "tencent-cvm",
      ready: false,
      missingEnv: ["OPL_VPC_ID"],
      missingTools: ["ansible-playbook"]
    })
  };
  const { origin, close } = await listen(createRequestHandler({
    appService,
    enforceSessionScope: false,
    enforceCsrf: false
  }));
  try {
    const response = await fetch(`${origin}/api/runtime/readiness`);
    const payload = await response.json();

    assert.equal(response.status, 200);
    assert.deepEqual(payload, {
      provider: "tencent-cvm",
      ready: false,
      missingEnv: ["OPL_VPC_ID"],
      missingTools: ["ansible-playbook"]
    });
  } finally {
    await close();
  }
});

test("production readiness route reports launch blockers without creating resources", async () => {
  const appService = {
    productionReadiness: async () => ({
      ready: false,
      missingEnv: ["DATABASE_URL"],
      missingTools: ["caddy"],
      failedChecks: ["database_url", "tools"],
      checks: []
    })
  };
  const { origin, close } = await listen(createRequestHandler({ appService }));
  try {
    const response = await fetch(`${origin}/api/production/readiness`);
    const payload = await response.json();

    assert.equal(response.status, 200);
    assert.deepEqual(payload, {
      ready: false,
      missingEnv: ["DATABASE_URL"],
      missingTools: ["caddy"],
      failedChecks: ["database_url", "tools"],
      checks: []
    });
  } finally {
    await close();
  }
});

test("runtime status route returns structured Workspace resource evidence without mutating resources", async () => {
  const requests = [];
  const appService = {
    runtimeStatus: async (input) => {
      requests.push(input);
      return {
        provider: "tencent-tke",
        workspaceId: input.workspaceId,
        ready: true,
        checks: [
          { name: "deployment_ready", ok: true },
          { name: "pvc_bound", ok: true },
          { name: "ingress_routes_workspace_url", ok: true }
        ]
      };
    }
  };
  const { origin, close } = await listen(createRequestHandler({
    appService,
    enforceSessionScope: false,
    enforceCsrf: false
  }));
  try {
    const response = await fetch(`${origin}/api/workspaces/runtime-status`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ accountId: "pi-route", workspaceId: "ws-route001" })
    });
    const payload = await response.json();

    assert.equal(response.status, 200);
    assert.deepEqual(requests, [{ accountId: "pi-route", workspaceId: "ws-route001" }]);
    assert.deepEqual(payload, {
      provider: "tencent-tke",
      workspaceId: "ws-route001",
      ready: true,
      checks: [
        { name: "deployment_ready", ok: true },
        { name: "pvc_bound", ok: true },
        { name: "ingress_routes_workspace_url", ok: true }
      ]
    });
  } finally {
    await close();
  }
});

test("operator summary route returns notification and failed operation aggregates without tokens", async () => {
  const appService = {
    operatorSummary: async (input) => ({
      product: "OPL Console",
      accountScope: input.accountId,
      workspaces: { total: 1, running: 0, needsAttention: 1 },
      notifications: {
        total: 1,
        error: 1,
        warning: 0,
        recent: [
          {
            id: "notification-1",
            accountId: "pi-route",
            workspaceId: "ws-route001",
            type: "workspace.create_failed",
            severity: "error",
            message: "image_pull_failed",
            createdAt: "2026-07-01T00:00:00.000Z"
          }
        ]
      },
      runtimeOperations: {
        total: 1,
        failed: 1,
        recentFailed: [
          {
            id: "op-1",
            accountId: "pi-route",
            workspaceId: "ws-route001",
            operationType: "create_workspace",
            error: "image_pull_failed",
            updatedAt: "2026-07-01T00:00:00.000Z"
          }
        ]
      }
    })
  };
  const { origin, close } = await listen(createRequestHandler({ appService, operatorSummaryToken: "operator-test-token" }));
  try {
    const blockedResponse = await fetch(`${origin}/api/operator/summary?accountId=pi-route`);
    assert.equal(blockedResponse.status, 403);

    const response = await fetch(`${origin}/api/operator/summary?accountId=pi-route`, {
      headers: { "x-opl-operator-token": "operator-test-token" }
    });
    const payload = await response.json();

    assert.equal(response.status, 200);
    assert.equal(payload.accountScope, "pi-route");
    assert.equal(payload.notifications.error, 1);
    assert.equal(payload.runtimeOperations.failed, 1);
    assert.equal(JSON.stringify(payload).includes("share_"), false);
  } finally {
    await close();
  }
});

test("console session route persists account profile and scopes default state", async () => {
  const appService = createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: { name: "test-provider" },
    pricing: {
      serverHourly: { basic: 1, pro: 4 },
      diskGbMonth: 0.2,
      markup: 0.2
    }
  });
  const { origin, close } = await listen(createRequestHandler({ appService }));
  try {
    const updateResponse = await fetch(`${origin}/api/session`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        accountId: "pi-session",
        tenantId: "tenant-session",
        displayName: "Session PI",
        email: "session@example.com",
        role: "pi"
      })
    });
    assert.equal(updateResponse.status, 200);
    const cookie = updateResponse.headers.get("set-cookie");
    assert.match(cookie, /opl_console_session=/);
    assert.match(cookie, /HttpOnly/);

    const sessionResponse = await fetch(`${origin}/api/session`, { headers: { cookie } });
    const session = await sessionResponse.json();
    assert.deepEqual(session, {
      accountId: "pi-session",
      tenantId: "tenant-session",
      displayName: "Session PI",
      email: "session@example.com",
      role: "pi"
    });

    const stateResponse = await fetch(`${origin}/api/state`, { headers: { cookie } });
    const state = await stateResponse.json();
    assert.equal(state.account.id, "pi-session");
    assert.equal(state.account.displayName, "Session PI");
  } finally {
    await close();
  }
});

test("console session cookies isolate account profiles across browser sessions", async () => {
  const appService = createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: { name: "test-provider" },
    pricing: {
      serverHourly: { basic: 1, pro: 4 },
      diskGbMonth: 0.2,
      markup: 0.2
    }
  });
  const { origin, close } = await listen(createRequestHandler({ appService }));
  try {
    const alphaResponse = await fetch(`${origin}/api/session`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        accountId: "pi-alpha-browser",
        tenantId: "tenant-alpha",
        displayName: "Alpha Browser PI",
        email: "alpha@example.com",
        role: "pi"
      })
    });
    const alphaCookie = alphaResponse.headers.get("set-cookie");

    const betaResponse = await fetch(`${origin}/api/session`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        accountId: "pi-beta-browser",
        tenantId: "tenant-beta",
        displayName: "Beta Browser PI",
        email: "beta@example.com",
        role: "pi"
      })
    });
    const betaCookie = betaResponse.headers.get("set-cookie");

    const alphaSession = await (await fetch(`${origin}/api/session`, { headers: { cookie: alphaCookie } })).json();
    const betaSession = await (await fetch(`${origin}/api/session`, { headers: { cookie: betaCookie } })).json();

    assert.equal(alphaSession.accountId, "pi-alpha-browser");
    assert.equal(alphaSession.tenantId, "tenant-alpha");
    assert.equal(betaSession.accountId, "pi-beta-browser");
    assert.equal(betaSession.tenantId, "tenant-beta");
  } finally {
    await close();
  }
});

test("configured console access token gates first PI login and preserves profile updates", async () => {
  const appService = createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: { name: "test-provider" },
    pricing: {
      serverHourly: { basic: 1, pro: 4 },
      diskGbMonth: 0.2,
      markup: 0.2
    }
  });
  const { origin, close } = await listen(createRequestHandler({
    appService,
    enforceSessionScope: true,
    consoleAccessToken: "pilot-login-token"
  }));
  try {
    const blockedResponse = await fetch(`${origin}/api/session`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        accountId: "pi-token",
        tenantId: "tenant-token",
        displayName: "Token PI",
        role: "pi"
      })
    });
    const blockedPayload = await blockedResponse.json();
    assert.equal(blockedResponse.status, 401);
    assert.equal(blockedPayload.error, "console_access_token_required");

    const loginResponse = await fetch(`${origin}/api/session`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        accountId: "pi-token",
        tenantId: "tenant-token",
        displayName: "Token PI",
        email: "token@example.com",
        accessToken: "pilot-login-token",
        role: "pi"
      })
    });
    const cookie = loginResponse.headers.get("set-cookie");
    assert.equal(loginResponse.status, 200);
    assert.match(cookie, /opl_console_session=/);

    const updateResponse = await fetch(`${origin}/api/session`, {
      method: "POST",
      headers: { "content-type": "application/json", cookie },
      body: JSON.stringify({
        accountId: "pi-token",
        tenantId: "tenant-other",
        displayName: "Updated Token PI",
        email: "updated-token@example.com",
        role: "pi"
      })
    });
    const updated = await updateResponse.json();
    assert.equal(updateResponse.status, 200);
    assert.deepEqual(updated, {
      accountId: "pi-token",
      tenantId: "tenant-token",
      displayName: "Updated Token PI",
      email: "updated-token@example.com",
      role: "pi"
    });
  } finally {
    await close();
  }
});

test("PI state hides runtime evidence while operator state keeps it", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-runtime-scope-"));
  const appService = createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: new LocalDockerProvider({
      rootDir: root,
      baseUrl: "http://127.0.0.1:8787",
      execute: false
    }),
    pricing: {
      serverHourly: { basic: 1, pro: 4 },
      diskGbMonth: 0.2,
      markup: 0.2
    }
  });
  await appService.creditAccount({ accountId: "pi-runtime", amount: 300, reason: "runtime_scope_seed" });
  await appService.createWorkspace({
    accountId: "pi-runtime",
    workspaceName: "Runtime Scope Lab",
    packageId: "basic"
  });

  const { origin, close } = await listen(createRequestHandler({
    appService,
    enforceSessionScope: true,
    consoleAccessToken: "pilot-login-token",
    operatorSummaryToken: "operator-token"
  }));
  try {
    const piLogin = await fetch(`${origin}/api/session`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        accountId: "pi-runtime",
        tenantId: "tenant-runtime",
        displayName: "Runtime PI",
        accessToken: "pilot-login-token",
        role: "pi"
      })
    });
    const piCookie = piLogin.headers.get("set-cookie");
    const piState = await (await fetch(`${origin}/api/state`, { headers: { cookie: piCookie } })).json();
    const piWorkspace = piState.workspaces[0];

    assert.equal(piWorkspace.docker, undefined);
    assert.equal(piWorkspace.server.localPath, undefined);
    assert.equal(piWorkspace.disk.localPath, undefined);
    assert.equal(piWorkspace.access.token, undefined);
    assert.deepEqual(piState.runtimeOperations, []);

    const operatorLogin = await fetch(`${origin}/api/session`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        accountId: "operator",
        tenantId: "ops",
        displayName: "Operator",
        operatorToken: "operator-token",
        role: "operator"
      })
    });
    const operatorCookie = operatorLogin.headers.get("set-cookie");
    const operatorState = await (await fetch(`${origin}/api/state?accountId=pi-runtime`, { headers: { cookie: operatorCookie } })).json();

    assert.match(operatorState.workspaces[0].docker.image, /one-person-lab-webui/);
    assert.ok(operatorState.workspaces[0].server.localPath);
    assert.equal(operatorState.runtimeOperations.length, 1);
  } finally {
    await close();
    await rm(root, { recursive: true, force: true });
  }
});

test("enforced console session scope blocks PI account switching and pilot credits", async () => {
  const appService = createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: { name: "test-provider" },
    pricing: {
      serverHourly: { basic: 1, pro: 4 },
      diskGbMonth: 0.2,
      markup: 0.2
    }
  });
  const { origin, close } = await listen(createRequestHandler({ appService, enforceSessionScope: true }));
  const loginResponse = await fetch(`${origin}/api/session`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      accountId: "pi-session",
      displayName: "Session PI",
      role: "pi"
    })
  });
  const cookie = loginResponse.headers.get("set-cookie");
  const csrf = loginResponse.headers.get("x-opl-csrf-token");
  try {
    const creditResponse = await fetch(`${origin}/api/accounts/credit`, {
      method: "POST",
      headers: { "content-type": "application/json", cookie, "x-opl-csrf-token": csrf },
      body: JSON.stringify({ accountId: "pi-other", amount: 200, reason: "ui_demo_credit" })
    });
    const creditPayload = await creditResponse.json();
    assert.equal(creditResponse.status, 403);
    assert.equal(creditPayload.error, "operator_role_required");

    const createResponse = await fetch(`${origin}/api/workspaces`, {
      method: "POST",
      headers: { "content-type": "application/json", cookie, "x-opl-csrf-token": csrf },
      body: JSON.stringify({ accountId: "pi-other", workspaceName: "Other Lab", packageId: "basic" })
    });
    const createPayload = await createResponse.json();
    assert.equal(createResponse.status, 403);
    assert.equal(createPayload.error, "account_scope_mismatch");
  } finally {
    await close();
  }
});
