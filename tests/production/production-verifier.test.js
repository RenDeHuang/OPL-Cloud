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

function tkeChain({ workspaceUrl = "https://workspace.medopl.cn/w/ws-tke-prod001/?token=share_tke_prod" } = {}) {
  const compute = {
    id: "compute-prod001",
    ownerAccountId: "pi-prod",
    packageId: "basic",
    provider: "tencent-tke",
    providerResourceId: "deployment/opl-compute-prod001",
    status: "running",
    billingStatus: "active",
    spec: "2c4g",
    image: "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:latest",
    runtime: { service: "service/opl-compute-prod001", serviceName: "opl-compute-prod001" }
  };
  const storage = {
    id: "storage-prod001",
    ownerAccountId: "pi-prod",
    packageId: "basic",
    provider: "tencent-tke",
    providerResourceId: "pvc/opl-storage-prod001-data",
    status: "available",
    billingStatus: "active",
    sizeGb: 10,
    storageClass: "cbs"
  };
  const attachment = {
    id: "attach-prod001",
    ownerAccountId: "pi-prod",
    computeAllocationId: compute.id,
    storageId: storage.id,
    mountPath: "/data",
    provider: "tencent-tke",
    providerAttachmentId: "deployment/opl-compute-prod001:pvc/opl-storage-prod001-data:/data",
    status: "attached"
  };
  const workspace = {
    id: "ws-tke-prod001",
    ownerAccountId: "pi-prod",
    name: "Production Verification Lab",
    packageId: "basic",
    state: "running",
    provider: "tencent-tke",
    computeAllocationId: compute.id,
    storageId: storage.id,
    attachmentId: attachment.id,
    server: { id: compute.providerResourceId, status: "running", billingStatus: "active", namespace: "opl-cloud", spec: "2c4g" },
    docker: { id: compute.providerResourceId, image: compute.image, status: "running", service: compute.runtime.service },
    disk: { id: storage.providerResourceId, status: "attached_retained", billingStatus: "active", sizeGb: 10, mountPath: "/data", storageClass: "cbs" },
    slug: "production-verification-lab",
    url: workspaceUrl,
    access: { token: "share_tke_prod", tokenStatus: "active", requiresLogin: false }
  };
  return { compute, storage, attachment, workspace };
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
      { name: "ingress_routes_workspace_gateway", ok: true }
    ]
  };
}

function chainResponses(chain) {
  return {
    "GET /api/production/readiness": { ready: true, missingEnv: [], missingTools: [], failedChecks: [], checks: [] },
    "GET /api/runtime/readiness": { provider: "tencent-tke", ready: true, missingEnv: [], missingTools: [] },
    "POST /api/billing/topups": { id: "pi-prod", balance: 1000, frozen: 0 },
    "POST /api/compute-allocations": chain.compute,
    "POST /api/storage-volumes": chain.storage,
    "POST /api/storage-attachments": chain.attachment,
    "POST /api/workspaces": chain.workspace,
    "POST /api/workspaces/runtime-status": readyRuntimeStatus(chain.workspace),
    [`GET ${chain.workspace.url}`]: "<html>one-person-lab-app</html>",
    "POST /api/billing/request-usage": {
      id: "usage-request-prod001",
      workspaceId: chain.workspace.id,
      accountId: "pi-prod",
      requestId: "production-verification-request",
      amount: 0.42
    },
    "GET /api/state": {
      wallet: { accountId: "pi-prod", balance: 999, frozen: 10 },
      billingLedger: [
        { id: "ledger-compute", accountId: "pi-prod", computeAllocationId: chain.compute.id, type: "compute_hold" },
        { id: "ledger-storage", accountId: "pi-prod", storageId: chain.storage.id, type: "storage_hold" },
        { id: "ledger-attach", accountId: "pi-prod", attachmentId: chain.attachment.id, type: "storage_attached" },
        { id: "ledger-request", accountId: "pi-prod", workspaceId: chain.workspace.id, type: "request_debit" }
      ],
      resourceUsageLogs: [
        { id: "usage-compute", accountId: "pi-prod", computeAllocationId: chain.compute.id },
        { id: "usage-storage", accountId: "pi-prod", storageId: chain.storage.id },
        { id: "usage-attach", accountId: "pi-prod", attachmentId: chain.attachment.id }
      ],
      requestUsageLogs: [
        { id: "usage-request-prod001", accountId: "pi-prod", workspaceId: chain.workspace.id, requestId: "production-verification-request" }
      ]
    },
    "POST /api/storage-attachments/detach": { ...chain.attachment, status: "detached" },
    [`POST /api/compute-allocations/${chain.compute.id}/destroy`]: { ...chain.compute, status: "destroyed", billingStatus: "stopped" },
    "POST /api/storage-volumes/destroy": { ...chain.storage, status: "destroyed", billingStatus: "stopped" }
  };
}

function keyedFetch({ responses, requests = [], responseHeaders = null }) {
  let workspaceUrlCount = 0;
  let runtimeStatusCount = 0;
  return async (url, options = {}) => {
    const parsed = new URL(String(url));
    const method = options.method || "GET";
    let key = parsed.origin === "https://console.oplcloud.cn" ? `${method} ${parsed.pathname}` : `${method} ${String(url)}`;
    if (key === "POST /api/workspaces/runtime-status") {
      runtimeStatusCount += 1;
      key = runtimeStatusCount === 1 ? key : `${key}#${runtimeStatusCount}`;
    }
    if (parsed.origin !== "https://console.oplcloud.cn") {
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

test("production verifier refuses localhost Console origins", async () => {
  await assert.rejects(
    verifyProductionChain({
      origin: "http://127.0.0.1:8791",
      fetchImpl: async () => {
        throw new Error("must_not_fetch");
      }
    }),
    /public_origin_required/
  );
});

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

test("production verifier exercises the public TKE resource provisioning chain", async () => {
  const requests = [];
  const chain = tkeChain();
  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    packageId: "basic",
    fetchImpl: keyedFetch({ responses: chainResponses(chain), requests })
  });

  assert.deepEqual(requests.map((request) => request.key), [
    "GET /api/production/readiness",
    "GET /api/runtime/readiness",
    "POST /api/billing/topups",
    "POST /api/compute-allocations",
    "POST /api/storage-volumes",
    "POST /api/storage-attachments",
    "POST /api/workspaces",
    "POST /api/workspaces/runtime-status",
    `GET ${chain.workspace.url}`,
    "POST /api/billing/request-usage",
    "GET /api/state",
    "POST /api/storage-attachments/detach",
    `POST /api/compute-allocations/${chain.compute.id}/destroy`,
    "POST /api/storage-volumes/destroy"
  ]);
  assert.deepEqual(requests.find((request) => request.key === "POST /api/workspaces").body, {
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    attachmentId: chain.attachment.id
  });
  assert.equal(requests.find((request) => request.key === "POST /api/storage-attachments").body.computeAllocationId, chain.compute.id);
  assert.equal(requests.find((request) => request.key === "POST /api/storage-attachments").body.storageId, chain.storage.id);
  assert.equal(result.workspaceId, chain.workspace.id);
  assert.equal(result.url, chain.workspace.url);
  assert.deepEqual(result.checks.map((check) => `${check.name}:${check.ok}`), [
    "production_readiness:true",
    "runtime_readiness:true",
    "compute_created:true",
    "storage_created:true",
    "storage_attached:true",
    "workspace_created:true",
    "workspace_runtime_status:true",
    "workspace_url:true",
    "request_usage_recorded:true",
    "ledger_and_usage_verified:true",
    "verification_storage_detached:true",
    "verification_compute_destroyed:true",
    "verification_storage_destroyed:true"
  ]);
});

test("production verifier authenticates as operator and sends CSRF on commercial write APIs", async () => {
  const requests = [];
  const chain = tkeChain();
  const responseHeaders = new Headers({
    "content-type": "application/json",
    "set-cookie": "opl_console_session=operator-session; Path=/; HttpOnly; SameSite=Lax",
    "x-opl-csrf-token": "csrf-auth"
  });
  const responses = {
    ...chainResponses(chain),
    "POST /api/auth/operator-login": { accountId: "operator", role: "operator" }
  };

  await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    operatorToken: "operator-token",
    fetchImpl: keyedFetch({ responses, requests, responseHeaders })
  });

  assert.deepEqual(requests.map((request) => request.key).slice(0, 4), [
    "GET /api/production/readiness",
    "GET /api/runtime/readiness",
    "POST /api/auth/operator-login",
    "POST /api/billing/topups"
  ]);
  for (const request of requests.filter((item) => item.key.startsWith("POST /api/") && item.key !== "POST /api/auth/operator-login")) {
    assert.match(request.cookie, /opl_console_session=operator-session/);
    assert.equal(request.csrf, "csrf-auth");
  }
});

test("production verifier retries TKE runtime status and Workspace URL until ready", async () => {
  const requests = [];
  const chain = tkeChain();
  const responses = chainResponses(chain);
  responses["POST /api/workspaces/runtime-status"] = {
    provider: "tencent-tke",
    workspaceId: chain.workspace.id,
    ready: false,
    checks: [{ name: "deployment_ready", ok: false }]
  };
  responses["POST /api/workspaces/runtime-status#2"] = readyRuntimeStatus(chain.workspace);
  responses[`GET ${chain.workspace.url}`] = "bad gateway";
  responses[`GET ${chain.workspace.url}#2`] = "<html>one-person-lab-app</html>";
  const baseFetch = keyedFetch({ responses, requests });

  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceUrlAttempts: 2,
    retryDelayMs: 0,
    fetchImpl: async (url, options = {}) => {
      const parsed = new URL(String(url));
      if (parsed.origin !== "https://console.oplcloud.cn") {
        const matching = requests.filter((request) => request.key.startsWith(`GET ${chain.workspace.url}`)).length;
        if (matching === 0) {
          requests.push({ key: `GET ${chain.workspace.url}`, body: null });
          return htmlResponse("bad gateway", 502);
        }
      }
      return baseFetch(url, options);
    }
  });

  assert.equal(result.checks.find((check) => check.name === "workspace_runtime_status").attempts, 2);
  assert.equal(result.checks.find((check) => check.name === "workspace_url").attempts, 2);
});

test("production verifier rejects localhost Workspace URLs and still cleans up resources", async () => {
  const requests = [];
  const chain = tkeChain({ workspaceUrl: "http://127.0.0.1:8791/workspaces/local?token=share_tke_prod" });

  await assert.rejects(
    verifyProductionChain({
      origin: "https://console.oplcloud.cn",
      accountId: "pi-prod",
      fetchImpl: keyedFetch({ responses: chainResponses(chain), requests })
    }),
    /public_workspace_url_required/
  );

  assert.deepEqual(requests.map((request) => request.key).slice(-3), [
    "POST /api/storage-attachments/detach",
    `POST /api/compute-allocations/${chain.compute.id}/destroy`,
    "POST /api/storage-volumes/destroy"
  ]);
});

test("production verifier reports cleanup failures without hiding the original verification failure", async () => {
  const chain = tkeChain();
  const responses = {
    ...chainResponses(chain),
    [`GET ${chain.workspace.url}`]: "bad gateway",
    "POST /api/storage-attachments/detach": { error: "detach_failed" },
    [`POST /api/compute-allocations/${chain.compute.id}/destroy`]: { error: "destroy_compute_failed" },
    "POST /api/storage-volumes/destroy": { error: "destroy_storage_failed" }
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
        const key = parsed.origin === "https://console.oplcloud.cn" ? `${method} ${parsed.pathname}` : `${method} ${String(url)}`;
        if (key === "POST /api/storage-attachments/detach") return jsonResponse(responses[key], 500);
        if (key === `POST /api/compute-allocations/${chain.compute.id}/destroy`) return jsonResponse(responses[key], 500);
        if (key === "POST /api/storage-volumes/destroy") return jsonResponse(responses[key], 500);
        const payload = responses[key];
        if (typeof payload === "string") return htmlResponse(payload, key.startsWith("GET https://") ? 502 : 200);
        if (payload) return jsonResponse(payload);
        throw new Error(`unexpected_request:${key}`);
      }
    });
  } catch (error) {
    caught = error;
  }

  assert.match(caught.message, /workspace_url_failed:502:bad gateway/);
  assert.deepEqual(caught.cleanupErrors, [
    "detach_storage:request_failed:POST:/api/storage-attachments/detach:500:detach_failed",
    `destroy_compute:request_failed:POST:/api/compute-allocations/${chain.compute.id}/destroy:500:destroy_compute_failed`,
    "destroy_storage:request_failed:POST:/api/storage-volumes/destroy:500:destroy_storage_failed"
  ]);
});

test("production verifier CLI writes structured failure JSON with cleanup errors", async () => {
  let stdout = "";
  let stderr = "";
  const chain = tkeChain();
  const responses = {
    ...chainResponses(chain),
    [`GET ${chain.workspace.url}`]: "bad gateway",
    "POST /api/storage-attachments/detach": { error: "detach_failed" },
    [`POST /api/compute-allocations/${chain.compute.id}/destroy`]: { error: "destroy_compute_failed" },
    "POST /api/storage-volumes/destroy": { error: "destroy_storage_failed" }
  };
  const code = await runProductionVerifierCli({
    argv: ["--origin", "https://console.oplcloud.cn", "--account", "pi-prod", "--run-id", "cli-fail", "--url-attempts", "1", "--retry-delay-ms", "0"],
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async (url, options = {}) => {
      const parsed = new URL(String(url));
      const method = options.method || "GET";
      const key = parsed.origin === "https://console.oplcloud.cn" ? `${method} ${parsed.pathname}` : `${method} ${String(url)}`;
      if (key === "POST /api/storage-attachments/detach") return jsonResponse(responses[key], 500);
      if (key === `POST /api/compute-allocations/${chain.compute.id}/destroy`) return jsonResponse(responses[key], 500);
      if (key === "POST /api/storage-volumes/destroy") return jsonResponse(responses[key], 500);
      const payload = responses[key];
      if (typeof payload === "string") return htmlResponse(payload, key.startsWith("GET https://") ? 502 : 200);
      if (payload) return jsonResponse(payload);
      throw new Error(`unexpected_request:${key}`);
    }
  });

  assert.equal(code, 1);
  assert.equal(stdout, "");
  assert.deepEqual(JSON.parse(stderr), {
    ok: false,
    error: "workspace_url_failed:502:bad gateway",
    cleanupErrors: [
      "detach_storage:request_failed:POST:/api/storage-attachments/detach:500:detach_failed",
      `destroy_compute:request_failed:POST:/api/compute-allocations/${chain.compute.id}/destroy:500:destroy_compute_failed`,
      "destroy_storage:request_failed:POST:/api/storage-volumes/destroy:500:destroy_storage_failed"
    ]
  });
});
