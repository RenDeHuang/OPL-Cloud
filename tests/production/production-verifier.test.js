import assert from "node:assert/strict";
import test from "node:test";

import { runProductionVerifierCli, verifyProductionChain } from "../../tools/production-verifier.js";

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
    ["GET https://production-verification-lab-prod001.oplcloud.cn/?token=share_prod#2", "<html>OPL Workspace restored</html>"],
    ["POST /api/billing/settle", { entries: [{ type: "server_debit" }, { type: "storage_debit" }], metering: [{ ok: true }] }],
    ["POST /api/workspaces/destroy-server#2", {
      ...workspace,
      state: "server_destroyed_disk_retained",
      server: { ...workspace.server, id: "ins-prod002", status: "destroyed", billingStatus: "stopped" },
      docker: { ...workspace.docker, status: "destroyed" },
      disk: { ...workspace.disk, status: "detached_retained" }
    }],
    ["POST /api/workspaces/destroy-disk", {
      ...workspace,
      state: "destroyed",
      server: { ...workspace.server, id: "ins-prod002", status: "destroyed", billingStatus: "stopped" },
      docker: { ...workspace.docker, status: "destroyed" },
      disk: { ...workspace.disk, status: "destroyed", billingStatus: "stopped" }
    }]
  ]);
  let restartCount = 0;
  let workspaceUrlCount = 0;
  let destroyServerCount = 0;

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
      if (key === "POST /api/workspaces/destroy-server") {
        destroyServerCount += 1;
        key = destroyServerCount === 1 ? key : `${key}#${destroyServerCount}`;
      }
      if (key === "GET https://production-verification-lab-prod001.oplcloud.cn/?token=share_prod") {
        workspaceUrlCount += 1;
        key = workspaceUrlCount === 1 ? key : `${key}#${workspaceUrlCount}`;
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
    "GET https://production-verification-lab-prod001.oplcloud.cn/?token=share_prod#2",
    "POST /api/billing/settle",
    "POST /api/workspaces/destroy-server#2",
    "POST /api/workspaces/destroy-disk"
  ]);
  assert.equal(requests.find((request) => request.key === "POST /api/workspaces").body.workspaceName, "Production Verification Lab");
  assert.equal(requests.find((request) => request.key === "POST /api/workspaces/destroy-server").body.confirm, true);
  assert.equal(requests.find((request) => request.key === "POST /api/workspaces/destroy-server#2").body.confirm, true);
  assert.equal(requests.find((request) => request.key === "POST /api/workspaces/destroy-disk").body.confirmDataLoss, true);
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
    "workspace_url_after_recreate:true",
    "billing_settlement:true",
    "verification_server_destroyed:true",
    "verification_disk_destroyed:true"
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
        "POST /api/workspaces/destroy-disk": { ...workspace, state: "destroyed", server: { ...workspace.server, status: "destroyed", billingStatus: "stopped" }, disk: { ...workspace.disk, status: "destroyed", billingStatus: "stopped" } },
        "POST /api/billing/settle": { entries: [{ type: "server_debit" }], metering: [{ ok: true }] }
      };
      return jsonResponse(responses[`${method} ${parsed.pathname}`]);
    }
  });

  assert.equal(workspaceUrlAttempts, 3);
  assert.equal(result.checks.find((check) => check.name === "workspace_url").attempts, 2);
  assert.equal(result.checks.find((check) => check.name === "workspace_url_after_recreate").attempts, 1);
});

test("production verifier destroys verification resources when a later check fails", async () => {
  const requests = [];
  const workspace = {
    id: "ws-prod003",
    ownerAccountId: "pi-prod",
    name: "Production Verification Lab",
    packageId: "basic",
    state: "running",
    provider: "tencent-cvm",
    server: { id: "ins-prod003", status: "running", billingStatus: "active" },
    docker: { id: "docker-prod003", status: "running" },
    disk: { id: "disk-prod003", status: "attached_retained", billingStatus: "active", mountPath: "/data" },
    slug: "production-verification-lab-prod003",
    url: "https://production-verification-lab-prod003.oplcloud.cn/?token=share_prod_fail",
    access: { token: "share_prod_fail", tokenStatus: "active", requiresLogin: false }
  };

  await assert.rejects(
    verifyProductionChain({
      origin: "https://console.oplcloud.cn",
      accountId: "pi-prod",
      workspaceUrlAttempts: 1,
      retryDelayMs: 0,
      fetchImpl: async (url, options = {}) => {
        const parsed = new URL(String(url));
        const method = options.method || "GET";
        const key = parsed.origin === "https://console.oplcloud.cn"
          ? `${method} ${parsed.pathname}`
          : `${method} ${String(url)}`;
        requests.push({ key, body: options.body ? JSON.parse(options.body) : null });

        if (key === "GET /api/production/readiness") return jsonResponse({ ready: true, missingEnv: [], missingTools: [], failedChecks: [], checks: [] });
        if (key === "GET /api/runtime/readiness") return jsonResponse({ provider: "tencent-cvm", ready: true, missingEnv: [], missingTools: [] });
        if (key === "POST /api/accounts/credit") return jsonResponse({ id: "pi-prod", balance: 1000, frozen: 0 });
        if (key === "POST /api/workspaces") return jsonResponse(workspace);
        if (key === "GET https://production-verification-lab-prod003.oplcloud.cn/?token=share_prod_fail") return htmlResponse("bad gateway", 502);
        if (key === "POST /api/workspaces/destroy-server") {
          return jsonResponse({
            ...workspace,
            state: "server_destroyed_disk_retained",
            server: { ...workspace.server, status: "destroyed", billingStatus: "stopped" },
            docker: { ...workspace.docker, status: "destroyed" },
            disk: { ...workspace.disk, status: "detached_retained" }
          });
        }
        if (key === "POST /api/workspaces/destroy-disk") {
          return jsonResponse({
            ...workspace,
            state: "destroyed",
            server: { ...workspace.server, status: "destroyed", billingStatus: "stopped" },
            docker: { ...workspace.docker, status: "destroyed" },
            disk: { ...workspace.disk, status: "destroyed", billingStatus: "stopped" }
          });
        }
        throw new Error(`unexpected_request:${key}`);
      }
    }),
    /workspace_url_failed:502:bad gateway/
  );

  assert.deepEqual(requests.map((request) => request.key), [
    "GET /api/production/readiness",
    "GET /api/runtime/readiness",
    "POST /api/accounts/credit",
    "POST /api/workspaces",
    "GET https://production-verification-lab-prod003.oplcloud.cn/?token=share_prod_fail",
    "POST /api/workspaces/destroy-server",
    "POST /api/workspaces/destroy-disk"
  ]);
  assert.equal(requests.find((request) => request.key === "POST /api/workspaces/destroy-server").body.confirm, true);
  assert.equal(requests.find((request) => request.key === "POST /api/workspaces/destroy-disk").body.confirmDataLoss, true);
});

test("production verifier reports cleanup failures without hiding the original verification failure", async () => {
  const workspace = {
    id: "ws-prod004",
    ownerAccountId: "pi-prod",
    name: "Production Verification Lab",
    packageId: "basic",
    state: "running",
    provider: "tencent-cvm",
    server: { id: "ins-prod004", status: "running", billingStatus: "active" },
    docker: { id: "docker-prod004", status: "running" },
    disk: { id: "disk-prod004", status: "attached_retained", billingStatus: "active", mountPath: "/data" },
    slug: "production-verification-lab-prod004",
    url: "https://production-verification-lab-prod004.oplcloud.cn/?token=share_prod_cleanup_fail",
    access: { token: "share_prod_cleanup_fail", tokenStatus: "active", requiresLogin: false }
  };
  let caught = null;

  try {
    await verifyProductionChain({
      origin: "https://console.oplcloud.cn",
      accountId: "pi-prod",
      workspaceUrlAttempts: 1,
      retryDelayMs: 0,
      fetchImpl: async (url, options = {}) => {
        const parsed = new URL(String(url));
        const method = options.method || "GET";
        const key = parsed.origin === "https://console.oplcloud.cn"
          ? `${method} ${parsed.pathname}`
          : `${method} ${String(url)}`;

        if (key === "GET /api/production/readiness") return jsonResponse({ ready: true, missingEnv: [], missingTools: [], failedChecks: [], checks: [] });
        if (key === "GET /api/runtime/readiness") return jsonResponse({ provider: "tencent-cvm", ready: true, missingEnv: [], missingTools: [] });
        if (key === "POST /api/accounts/credit") return jsonResponse({ id: "pi-prod", balance: 1000, frozen: 0 });
        if (key === "POST /api/workspaces") return jsonResponse(workspace);
        if (key === "GET https://production-verification-lab-prod004.oplcloud.cn/?token=share_prod_cleanup_fail") return htmlResponse("bad gateway", 502);
        if (key === "POST /api/workspaces/destroy-server") return jsonResponse({ error: "destroy_server_failed" }, 500);
        if (key === "POST /api/workspaces/destroy-disk") return jsonResponse({ error: "destroy_disk_failed" }, 500);
        throw new Error(`unexpected_request:${key}`);
      }
    });
  } catch (error) {
    caught = error;
  }

  assert.match(caught.message, /workspace_url_failed:502:bad gateway/);
  assert.deepEqual(caught.cleanupErrors, [
    "destroy_server:request_failed:POST:/api/workspaces/destroy-server:500:destroy_server_failed",
    "destroy_disk:request_failed:POST:/api/workspaces/destroy-disk:500:destroy_disk_failed"
  ]);
});

test("production verifier uses a unique default Workspace name for each run", async () => {
  const workspaceNames = [];
  const creditReasons = [];
  const settlementSourceEventIds = [];
  const cleanupWorkspace = (workspace) => ({
    ...workspace,
    state: "destroyed",
    server: { ...workspace.server, status: "destroyed", billingStatus: "stopped" },
    docker: { ...workspace.docker, status: "destroyed" },
    disk: { ...workspace.disk, status: "destroyed", billingStatus: "stopped" }
  });

  async function runVerifier(runSuffix) {
    const workspace = {
      id: `ws-${runSuffix}`,
      ownerAccountId: "pi-prod",
      name: `Production Verification Lab ${runSuffix}`,
      packageId: "basic",
      state: "running",
      provider: "tencent-cvm",
      server: { id: `ins-${runSuffix}`, status: "running", billingStatus: "active" },
      docker: { id: `docker-${runSuffix}`, status: "running" },
      disk: { id: `disk-${runSuffix}`, status: "attached_retained", billingStatus: "active", mountPath: "/data" },
      slug: `production-verification-lab-${runSuffix}`,
      url: `https://production-verification-lab-${runSuffix}.oplcloud.cn/?token=share_${runSuffix}`,
      access: { token: `share_${runSuffix}`, tokenStatus: "active", requiresLogin: false }
    };

    await verifyProductionChain({
      origin: "https://console.oplcloud.cn",
      accountId: "pi-prod",
      runId: runSuffix,
      retryDelayMs: 0,
      fetchImpl: async (url, options = {}) => {
        const parsed = new URL(String(url));
        const method = options.method || "GET";
        if (parsed.origin !== "https://console.oplcloud.cn") return htmlResponse("<html>OPL Workspace</html>");
        if (`${method} ${parsed.pathname}` === "GET /api/production/readiness") return jsonResponse({ ready: true, missingEnv: [], missingTools: [], failedChecks: [], checks: [] });
        if (`${method} ${parsed.pathname}` === "GET /api/runtime/readiness") return jsonResponse({ provider: "tencent-cvm", ready: true, missingEnv: [], missingTools: [] });
        if (`${method} ${parsed.pathname}` === "POST /api/accounts/credit") {
          creditReasons.push(JSON.parse(options.body).reason);
          return jsonResponse({ id: "pi-prod", balance: 1000, frozen: 0 });
        }
        if (`${method} ${parsed.pathname}` === "POST /api/workspaces") {
          const body = JSON.parse(options.body);
          workspaceNames.push(body.workspaceName);
          return jsonResponse({ ...workspace, name: body.workspaceName });
        }
        if (`${method} ${parsed.pathname}` === "POST /api/workspaces/stop-server") return jsonResponse({ ...workspace, state: "stopped_server_disk_retained", server: { ...workspace.server, status: "stopped", billingStatus: "stopped" } });
        if (`${method} ${parsed.pathname}` === "POST /api/workspaces/restart-server") return jsonResponse(workspace);
        if (`${method} ${parsed.pathname}` === "POST /api/workspaces/destroy-server") return jsonResponse({ ...workspace, state: "server_destroyed_disk_retained", server: { ...workspace.server, status: "destroyed", billingStatus: "stopped" }, disk: { ...workspace.disk, status: "detached_retained" } });
        if (`${method} ${parsed.pathname}` === "POST /api/billing/settle") {
          settlementSourceEventIds.push(JSON.parse(options.body).sourceEventId);
          return jsonResponse({ entries: [{ type: "server_debit" }, { type: "storage_debit" }], metering: [{ ok: true }] });
        }
        if (`${method} ${parsed.pathname}` === "POST /api/workspaces/destroy-disk") return jsonResponse(cleanupWorkspace(workspace));
        throw new Error(`unexpected_request:${method} ${parsed.pathname}`);
      }
    });
  }

  await runVerifier("run-a1");
  await runVerifier("run-b2");

  assert.deepEqual(workspaceNames, [
    "Production Verification Lab run-a1",
    "Production Verification Lab run-b2"
  ]);
  assert.deepEqual(creditReasons, [
    "production_verification_credit:run-a1",
    "production_verification_credit:run-b2"
  ]);
  assert.deepEqual(settlementSourceEventIds, [
    "production_verification_tick:run-a1",
    "production_verification_tick:run-b2"
  ]);
});

test("production verifier CLI writes structured failure JSON with cleanup errors", async () => {
  let stdout = "";
  let stderr = "";
  const code = await runProductionVerifierCli({
    argv: ["--origin", "https://console.oplcloud.cn", "--account", "pi-prod", "--run-id", "cli-fail", "--url-attempts", "1", "--retry-delay-ms", "0"],
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async (url, options = {}) => {
      const parsed = new URL(String(url));
      const method = options.method || "GET";
      const key = parsed.origin === "https://console.oplcloud.cn"
        ? `${method} ${parsed.pathname}`
        : `${method} ${String(url)}`;
      const workspace = {
        id: "ws-cli-fail",
        ownerAccountId: "pi-prod",
        name: "Production Verification Lab cli-fail",
        packageId: "basic",
        state: "running",
        provider: "tencent-cvm",
        server: { id: "ins-cli-fail", status: "running", billingStatus: "active" },
        docker: { id: "docker-cli-fail", status: "running" },
        disk: { id: "disk-cli-fail", status: "attached_retained", billingStatus: "active", mountPath: "/data" },
        slug: "production-verification-lab-cli-fail",
        url: "https://production-verification-lab-cli-fail.oplcloud.cn/?token=share_cli_fail",
        access: { token: "share_cli_fail", tokenStatus: "active", requiresLogin: false }
      };

      if (key === "GET /api/production/readiness") return jsonResponse({ ready: true, missingEnv: [], missingTools: [], failedChecks: [], checks: [] });
      if (key === "GET /api/runtime/readiness") return jsonResponse({ provider: "tencent-cvm", ready: true, missingEnv: [], missingTools: [] });
      if (key === "POST /api/accounts/credit") return jsonResponse({ id: "pi-prod", balance: 1000, frozen: 0 });
      if (key === "POST /api/workspaces") return jsonResponse(workspace);
      if (key === "GET https://production-verification-lab-cli-fail.oplcloud.cn/?token=share_cli_fail") return htmlResponse("bad gateway", 502);
      if (key === "POST /api/workspaces/destroy-server") return jsonResponse({ error: "destroy_server_failed" }, 500);
      if (key === "POST /api/workspaces/destroy-disk") return jsonResponse({ error: "destroy_disk_failed" }, 500);
      throw new Error(`unexpected_request:${key}`);
    }
  });

  assert.equal(code, 1);
  assert.equal(stdout, "");
  const payload = JSON.parse(stderr);
  assert.deepEqual(payload, {
    ok: false,
    error: "workspace_url_failed:502:bad gateway",
    cleanupErrors: [
      "destroy_server:request_failed:POST:/api/workspaces/destroy-server:500:destroy_server_failed",
      "destroy_disk:request_failed:POST:/api/workspaces/destroy-disk:500:destroy_disk_failed"
    ]
  });
});
