import assert from "node:assert/strict";
import test from "node:test";

import { runProductionVerifierCli, verifyProductionChain } from "../../tools/production-verifier.js";

function jsonResponse(payload, status = 200, headers = new Headers({ "content-type": "application/json" })) {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers,
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

function tkeWorkspace({ id = "ws-tke-prod001", token = "share_tke_prod", name = "Production Verification Lab" } = {}) {
  const runtimeName = `opl-ws-${id.replace(/^ws-/, "")}`;
  return {
    id,
    ownerAccountId: "pi-prod",
    name,
    packageId: "basic",
    state: "running",
    provider: "tencent-tke",
    server: { id: `deployment/${runtimeName}`, status: "running", billingStatus: "active", namespace: "opl-cloud", spec: "2c4g" },
    docker: {
      id: `deployment/${runtimeName}`,
      image: "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:latest",
      status: "running",
      service: `service/${runtimeName}`
    },
    disk: {
      id: `pvc/${runtimeName}-data`,
      status: "attached_retained",
      billingStatus: "active",
      sizeGb: 10,
      mountPath: "/data",
      storageClass: "cbs"
    },
    slug: name.toLowerCase().replace(/[^a-z0-9]+/g, "-"),
    url: `https://workspace.medopl.cn/w/${id}?token=${token}`,
    access: { token, tokenStatus: "active", requiresLogin: false }
  };
}

function readyRuntimeStatus(workspace) {
  return {
    provider: "tencent-tke",
    workspaceId: workspace.id,
    ready: true,
    checks: [
      { name: "deployment_ready", ok: true },
      { name: "workspace_image_pulled", ok: true },
      { name: "pvc_bound", ok: true },
      { name: "deployment_uses_retained_pvc", ok: true },
      { name: "service_targets_workspace", ok: true },
      { name: "service_endpoints_ready", ok: true },
      { name: "ingress_routes_workspace_url", ok: true }
    ]
  };
}

function tkeLifecycleResponses(workspace) {
  return {
    "GET /api/production/readiness": { ready: true, missingEnv: [], missingTools: [], failedChecks: [], checks: [] },
    "GET /api/runtime/readiness": { provider: "tencent-tke", ready: true, missingEnv: [], missingTools: [] },
    "POST /api/billing/topups": { id: "pi-prod", balance: 1000, frozen: 0 },
    "POST /api/workspaces": workspace,
    "POST /api/workspaces/runtime-status": readyRuntimeStatus(workspace),
    [`GET ${workspace.url}`]: "<html>OPL Workspace</html>",
    "POST /api/workspaces/stop-server": {
      ...workspace,
      state: "stopped_server_disk_retained",
      server: { ...workspace.server, status: "stopped", billingStatus: "stopped" },
      disk: { ...workspace.disk, status: "attached_retained" }
    },
    "POST /api/workspaces/restart-server#1": workspace,
    "POST /api/workspaces/destroy-server": {
      ...workspace,
      state: "server_destroyed_disk_retained",
      server: { ...workspace.server, status: "destroyed", billingStatus: "stopped" },
      docker: { ...workspace.docker, status: "destroyed" },
      disk: { ...workspace.disk, status: "detached_retained" }
    },
    "POST /api/workspaces/restart-server#2": workspace,
    [`GET ${workspace.url}#2`]: "<html>OPL Workspace restored</html>",
    "POST /api/billing/settle": { entries: [{ type: "compute_debit" }, { type: "storage_debit" }] },
    "POST /api/workspaces/destroy-server#2": {
      ...workspace,
      state: "server_destroyed_disk_retained",
      server: { ...workspace.server, status: "destroyed", billingStatus: "stopped" },
      docker: { ...workspace.docker, status: "destroyed" },
      disk: { ...workspace.disk, status: "detached_retained" }
    },
    "POST /api/workspaces/destroy-disk": {
      ...workspace,
      state: "destroyed",
      server: { ...workspace.server, status: "destroyed", billingStatus: "stopped" },
      docker: { ...workspace.docker, status: "destroyed" },
      disk: { ...workspace.disk, status: "destroyed", billingStatus: "stopped" }
    }
  };
}

function keyedFetch({ responses, requests = [], responseHeaders = null }) {
  let restartCount = 0;
  let destroyServerCount = 0;
  let workspaceUrlCount = 0;
  let runtimeStatusCount = 0;
  return async (url, options = {}) => {
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
    if (key === "POST /api/workspaces/runtime-status") {
      runtimeStatusCount += 1;
      key = runtimeStatusCount === 1 ? key : `${key}#${runtimeStatusCount}`;
    }
    if (parsed.origin !== "https://console.oplcloud.cn" && !key.endsWith("#2")) {
      workspaceUrlCount += 1;
      key = workspaceUrlCount === 1 ? key : `${key}#${workspaceUrlCount}`;
    }
    requests.push({
      key,
      cookie: options.headers?.cookie || "",
      csrf: options.headers?.["x-opl-csrf-token"] || "",
      body: options.body ? JSON.parse(options.body) : null
    });
    const payload = responses[key] ?? responses[key.replace(/#1$/, "")];
    if (typeof payload === "string") return htmlResponse(payload);
    if (payload) {
      if (key === "POST /api/auth/operator-login" && responseHeaders) return jsonResponse(payload, 200, responseHeaders);
      return jsonResponse(payload);
    }
    throw new Error(`unexpected_request:${key}`);
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
            missingTools: ["kubectl"],
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

test("production verifier exercises the full Tencent TKE Workspace lifecycle through the deployed API", async () => {
  const requests = [];
  const workspace = tkeWorkspace();
  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    packageId: "basic",
    fetchImpl: keyedFetch({ responses: tkeLifecycleResponses(workspace), requests })
  });

  assert.deepEqual(requests.map((request) => request.key), [
    "GET /api/production/readiness",
    "GET /api/runtime/readiness",
    "POST /api/billing/topups",
    "POST /api/workspaces",
    "POST /api/workspaces/runtime-status",
    `GET ${workspace.url}`,
    "POST /api/workspaces/stop-server",
    "POST /api/workspaces/restart-server#1",
    "POST /api/workspaces/destroy-server",
    "POST /api/workspaces/restart-server#2",
    `GET ${workspace.url}#2`,
    "POST /api/billing/settle",
    "POST /api/workspaces/destroy-server#2",
    "POST /api/workspaces/destroy-disk"
  ]);
  assert.equal(requests.find((request) => request.key === "POST /api/workspaces").body.workspaceName, "Production Verification Lab");
  assert.equal(requests.find((request) => request.key === "POST /api/workspaces/destroy-server").body.confirm, true);
  assert.equal(requests.find((request) => request.key === "POST /api/workspaces/destroy-disk").body.confirmDataLoss, true);
  assert.equal(result.workspaceId, workspace.id);
  assert.equal(result.url, workspace.url);
  assert.deepEqual(result.checks.map((check) => `${check.name}:${check.ok}`), [
    "production_readiness:true",
    "runtime_readiness:true",
    "workspace_created:true",
    "workspace_runtime_status:true",
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

test("production verifier authenticates as operator and sends CSRF on commercial write APIs", async () => {
  const requests = [];
  const workspace = tkeWorkspace({ id: "ws-tke-auth", token: "share_tke_auth" });
  const responseHeaders = new Headers({
    "content-type": "application/json",
    "set-cookie": "opl_console_session=operator-session; Path=/; HttpOnly; SameSite=Lax",
    "x-opl-csrf-token": "csrf-auth"
  });
  const responses = {
    ...tkeLifecycleResponses(workspace),
    "POST /api/auth/operator-login": { accountId: "operator", role: "operator" }
  };

  await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    packageId: "basic",
    operatorToken: "operator-token",
    fetchImpl: keyedFetch({ responses, requests, responseHeaders })
  });

  assert.deepEqual(requests.map((request) => request.key).slice(0, 4), [
    "GET /api/production/readiness",
    "GET /api/runtime/readiness",
    "POST /api/auth/operator-login",
    "POST /api/billing/topups"
  ]);
  assert.equal(requests.find((request) => request.key === "POST /api/auth/operator-login").body.operatorToken, "operator-token");
  for (const request of requests.filter((item) => item.key.startsWith("POST /api/") && item.key !== "POST /api/auth/operator-login")) {
    assert.match(request.cookie, /opl_console_session=operator-session/);
    assert.equal(request.csrf, "csrf-auth");
  }
});

test("production verifier retries TKE runtime status and Workspace URL until ready", async () => {
  const requests = [];
  const workspace = tkeWorkspace({ id: "ws-tke-slow", token: "share_tke_slow" });
  const responses = tkeLifecycleResponses(workspace);
  responses["POST /api/workspaces/runtime-status"] = {
    provider: "tencent-tke",
    workspaceId: workspace.id,
    ready: false,
    checks: [
      { name: "deployment_ready", ok: false },
      { name: "pvc_bound", ok: true }
    ]
  };
  responses["POST /api/workspaces/runtime-status#2"] = readyRuntimeStatus(workspace);
  responses[`GET ${workspace.url}`] = "bad gateway";
  responses[`GET ${workspace.url}#2`] = "<html>OPL Workspace</html>";
  responses[`GET ${workspace.url}#3`] = "<html>OPL Workspace restored</html>";
  const baseFetch = keyedFetch({ responses, requests });

  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceUrlAttempts: 2,
    retryDelayMs: 0,
    fetchImpl: async (url, options = {}) => {
      const parsed = new URL(String(url));
      if (parsed.origin !== "https://console.oplcloud.cn" && !String(url).endsWith("#2")) {
        const matching = requests.filter((request) => request.key.startsWith(`GET ${workspace.url}`)).length;
        if (matching === 0) {
          requests.push({ key: `GET ${workspace.url}`, body: null });
          return htmlResponse("bad gateway", 502);
        }
      }
      return baseFetch(url, options);
    }
  });

  assert.equal(result.checks.find((check) => check.name === "workspace_runtime_status").attempts, 2);
  assert.equal(result.checks.find((check) => check.name === "workspace_url").attempts, 2);
});

test("production verifier reports cleanup failures without hiding the original verification failure", async () => {
  const workspace = tkeWorkspace({ id: "ws-tke-cleanup", token: "share_tke_cleanup" });
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
        if (key === "GET /api/runtime/readiness") return jsonResponse({ provider: "tencent-tke", ready: true, missingEnv: [], missingTools: [] });
        if (key === "POST /api/billing/topups") return jsonResponse({ id: "pi-prod", balance: 1000, frozen: 0 });
        if (key === "POST /api/workspaces") return jsonResponse(workspace);
        if (key === "POST /api/workspaces/runtime-status") return jsonResponse(readyRuntimeStatus(workspace));
        if (key === `GET ${workspace.url}`) return htmlResponse("bad gateway", 502);
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

test("production verifier CLI writes structured failure JSON with cleanup errors", async () => {
  let stdout = "";
  let stderr = "";
  const workspace = tkeWorkspace({ id: "ws-tke-cli-fail", token: "share_tke_cli_fail", name: "Production Verification Lab cli-fail" });
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

      if (key === "GET /api/production/readiness") return jsonResponse({ ready: true, missingEnv: [], missingTools: [], failedChecks: [], checks: [] });
      if (key === "GET /api/runtime/readiness") return jsonResponse({ provider: "tencent-tke", ready: true, missingEnv: [], missingTools: [] });
      if (key === "POST /api/billing/topups") return jsonResponse({ id: "pi-prod", balance: 1000, frozen: 0 });
      if (key === "POST /api/workspaces") return jsonResponse(workspace);
      if (key === "POST /api/workspaces/runtime-status") return jsonResponse(readyRuntimeStatus(workspace));
      if (key === `GET ${workspace.url}`) return htmlResponse("bad gateway", 502);
      if (key === "POST /api/workspaces/destroy-server") return jsonResponse({ error: "destroy_server_failed" }, 500);
      if (key === "POST /api/workspaces/destroy-disk") return jsonResponse({ error: "destroy_disk_failed" }, 500);
      throw new Error(`unexpected_request:${key}`);
    }
  });

  assert.equal(code, 1);
  assert.equal(stdout, "");
  assert.deepEqual(JSON.parse(stderr), {
    ok: false,
    error: "workspace_url_failed:502:bad gateway",
    cleanupErrors: [
      "destroy_server:request_failed:POST:/api/workspaces/destroy-server:500:destroy_server_failed",
      "destroy_disk:request_failed:POST:/api/workspaces/destroy-disk:500:destroy_disk_failed"
    ]
  });
});
