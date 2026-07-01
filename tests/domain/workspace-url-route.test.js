import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { mkdtemp, rm } from "node:fs/promises";
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
      markup: 0.1
    }
  });
  const { origin, close } = await listen(createRequestHandler({ appService }));
  try {
    await appService.creditAccount({ accountId: "pi-route", amount: 200, reason: "route_test_credit" });
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
    assert.match(html, /docker-compose\.yml|runtime target/);
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
  const { origin, close } = await listen(createRequestHandler({ appService }));
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
  const { origin, close } = await listen(createRequestHandler({ appService }));
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
