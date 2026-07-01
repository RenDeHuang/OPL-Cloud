import assert from "node:assert/strict";
import test from "node:test";

import { verifyProductionChain } from "../tools/production-verifier.js";

function jsonResponse(payload, status = 200) {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: new Headers({ "content-type": "application/json" }),
    json: async () => payload,
    text: async () => JSON.stringify(payload)
  };
}

function htmlResponse(html, status = 200) {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: new Headers({ "content-type": "text/html" }),
    json: async () => JSON.parse(html),
    text: async () => html
  };
}

test("production verifier refuses to create resources until readiness gates are ready", async () => {
  const requests = [];

  await assert.rejects(
    verifyProductionChain({
      origin: "https://console.oplcloud.cn",
      fetchImpl: async (url, options = {}) => {
        requests.push({ url: String(url), method: options.method || "GET" });
        if (String(url).endsWith("/api/production/readiness")) {
          return jsonResponse({
            ready: false,
            missingEnv: ["DATABASE_URL"],
            missingTools: ["tccli"],
            failedChecks: ["database_url", "tools"],
            checks: []
          });
        }
        throw new Error(`unexpected_request:${url}`);
      }
    }),
    /production_readiness_not_ready:database_url,tools/
  );

  assert.deepEqual(requests, [
    { url: "https://console.oplcloud.cn/api/production/readiness", method: "GET" }
  ]);
});

test("production verifier exercises the full Workspace cloud lifecycle through the deployed API", async () => {
  const requests = [];
  const workspace = {
    id: "ws-prod001",
    ownerAccountId: "pi-prod",
    name: "Production Verification Lab",
    packageId: "basic",
    state: "running",
    provider: "tencent-cvm",
    server: { id: "ins-prod001", status: "running", billingStatus: "active" },
    docker: { id: "docker-prod001", status: "running" },
    disk: { id: "disk-prod001", status: "attached_retained", billingStatus: "active", mountPath: "/data" },
    slug: "production-verification-lab-prod001",
    url: "https://production-verification-lab-prod001.oplcloud.cn/?token=share_prod",
    access: { token: "share_prod", tokenStatus: "active", requiresLogin: false }
  };

  const responses = new Map([
    ["GET /api/production/readiness", { ready: true, missingEnv: [], missingTools: [], failedChecks: [], checks: [] }],
    ["GET /api/runtime/readiness", { provider: "tencent-cvm", ready: true, missingEnv: [], missingTools: [] }],
    ["POST /api/accounts/credit", { id: "pi-prod", balance: 1000, frozen: 0 }],
    ["POST /api/workspaces", workspace],
    ["GET https://production-verification-lab-prod001.oplcloud.cn/?token=share_prod", "<html>OPL Workspace</html>"],
    ["POST /api/workspaces/stop-server", {
      ...workspace,
      state: "stopped_server_disk_retained",
      server: { ...workspace.server, status: "stopped", billingStatus: "stopped" },
      disk: { ...workspace.disk, status: "attached_retained" }
    }],
    ["POST /api/workspaces/restart-server#1", workspace],
    ["POST /api/workspaces/destroy-server", {
      ...workspace,
      state: "server_destroyed_disk_retained",
      server: { ...workspace.server, status: "destroyed", billingStatus: "stopped" },
      docker: { ...workspace.docker, status: "destroyed" },
      disk: { ...workspace.disk, status: "detached_retained" }
    }],
    ["POST /api/workspaces/restart-server#2", {
      ...workspace,
      server: { ...workspace.server, id: "ins-prod002" },
      disk: { ...workspace.disk, id: "disk-prod001", status: "attached_retained" }
    }],
    ["POST /api/billing/settle", { entries: [{ type: "server_debit" }, { type: "storage_debit" }], metering: [{ ok: true }] }]
  ]);
  let restartCount = 0;

  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    packageId: "basic",
    fetchImpl: async (url, options = {}) => {
      const parsed = new URL(String(url));
      const method = options.method || "GET";
      const pathname = parsed.origin === "https://console.oplcloud.cn" ? parsed.pathname : String(url);
      let key = `${method} ${pathname}`;
      if (key === "POST /api/workspaces/restart-server") {
        restartCount += 1;
        key = `${key}#${restartCount}`;
      }
      requests.push({ key, body: options.body ? JSON.parse(options.body) : null });
      const payload = responses.get(key);
      if (typeof payload === "string") return htmlResponse(payload);
      if (payload) return jsonResponse(payload);
      throw new Error(`unexpected_request:${key}`);
    }
  });

  assert.deepEqual(requests.map((request) => request.key), [
    "GET /api/production/readiness",
    "GET /api/runtime/readiness",
    "POST /api/accounts/credit",
    "POST /api/workspaces",
    "GET https://production-verification-lab-prod001.oplcloud.cn/?token=share_prod",
    "POST /api/workspaces/stop-server",
    "POST /api/workspaces/restart-server#1",
    "POST /api/workspaces/destroy-server",
    "POST /api/workspaces/restart-server#2",
    "POST /api/billing/settle"
  ]);
  assert.equal(requests.find((request) => request.key === "POST /api/workspaces").body.workspaceName, "Production Verification Lab");
  assert.equal(requests.find((request) => request.key === "POST /api/workspaces/destroy-server").body.confirm, true);
  assert.equal(result.workspaceId, "ws-prod001");
  assert.equal(result.url, workspace.url);
  assert.deepEqual(result.checks.map((check) => `${check.name}:${check.ok}`), [
    "production_readiness:true",
    "runtime_readiness:true",
    "workspace_created:true",
    "workspace_url:true",
    "server_stopped_storage_retained:true",
    "server_restarted:true",
    "server_destroyed_storage_retained:true",
    "server_recreated_from_retained_disk:true",
    "billing_settlement:true"
  ]);
});

test("production verifier retries the Workspace URL while Caddy and Docker become ready", async () => {
  const workspace = {
    id: "ws-prod002",
    ownerAccountId: "pi-prod",
    name: "Production Verification Lab",
    packageId: "basic",
    state: "running",
    provider: "tencent-cvm",
    server: { id: "ins-prod002", status: "running", billingStatus: "active" },
    docker: { id: "docker-prod002", status: "running" },
    disk: { id: "disk-prod002", status: "attached_retained", billingStatus: "active", mountPath: "/data" },
    slug: "production-verification-lab-prod002",
    url: "https://production-verification-lab-prod002.oplcloud.cn/?token=share_prod_retry",
    access: { token: "share_prod_retry", tokenStatus: "active", requiresLogin: false }
  };
  let workspaceUrlAttempts = 0;

  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceUrlAttempts: 2,
    retryDelayMs: 0,
    fetchImpl: async (url, options = {}) => {
      const parsed = new URL(String(url));
      const method = options.method || "GET";
      if (parsed.origin !== "https://console.oplcloud.cn") {
        workspaceUrlAttempts += 1;
        return workspaceUrlAttempts === 1
          ? htmlResponse("bad gateway", 502)
          : htmlResponse("<html>OPL Workspace</html>");
      }

      const responses = {
        "GET /api/production/readiness": { ready: true, missingEnv: [], missingTools: [], failedChecks: [], checks: [] },
        "GET /api/runtime/readiness": { provider: "tencent-cvm", ready: true, missingEnv: [], missingTools: [] },
        "POST /api/accounts/credit": { id: "pi-prod", balance: 1000, frozen: 0 },
        "POST /api/workspaces": workspace,
        "POST /api/workspaces/stop-server": { ...workspace, state: "stopped_server_disk_retained", server: { ...workspace.server, status: "stopped", billingStatus: "stopped" } },
        "POST /api/workspaces/restart-server": workspace,
        "POST /api/workspaces/destroy-server": { ...workspace, state: "server_destroyed_disk_retained", server: { ...workspace.server, status: "destroyed", billingStatus: "stopped" }, disk: { ...workspace.disk, status: "detached_retained" } },
        "POST /api/billing/settle": { entries: [{ type: "server_debit" }], metering: [{ ok: true }] }
      };
      return jsonResponse(responses[`${method} ${parsed.pathname}`]);
    }
  });

  assert.equal(workspaceUrlAttempts, 2);
  assert.equal(result.checks.find((check) => check.name === "workspace_url").attempts, 2);
});
