import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";

import { verifyProductionPersistenceChain } from "../../tools/production-persistence-e2e.js";

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

function resourceChain(runId) {
  const computeA = {
    id: "compute-a",
    ownerAccountId: "pi-e2e",
    provider: "tencent-tke",
    providerResourceId: "nodepool/np-compute-a",
    nodePoolId: "np-compute-a",
    status: "running",
    billingStatus: "active",
    runtime: {
      service: "service/opl-compute-a",
      serviceName: "opl-compute-a",
      workloadName: "opl-compute-a",
      nodeSelector: { "oplcloud.cn/compute-id": "compute-a" }
    }
  };
  const computeB = {
    ...computeA,
    id: "compute-b",
    providerResourceId: "nodepool/np-compute-b",
    nodePoolId: "np-compute-b",
    runtime: {
      service: "service/opl-compute-b",
      serviceName: "opl-compute-b",
      workloadName: "opl-compute-b",
      nodeSelector: { "oplcloud.cn/compute-id": "compute-b" }
    }
  };
  const storage = {
    id: "storage-persist",
    ownerAccountId: "pi-e2e",
    provider: "tencent-tke",
    providerResourceId: "pvc/opl-storage-persist-data",
    status: "available",
    billingStatus: "active",
    sizeGb: 10
  };
  const attachmentA = {
    id: "attach-a",
    ownerAccountId: "pi-e2e",
    computeId: computeA.id,
    storageId: storage.id,
    mountPath: "/data",
    provider: "tencent-tke",
    providerAttachmentId: "deployment/opl-compute-a:pvc/opl-storage-persist-data:/data",
    status: "attached"
  };
  const attachmentB = {
    ...attachmentA,
    id: "attach-b",
    computeId: computeB.id,
    providerAttachmentId: "deployment/opl-compute-b:pvc/opl-storage-persist-data:/data"
  };
  const workspaceA = {
    id: "ws-a",
    ownerAccountId: "pi-e2e",
    provider: "tencent-tke",
    computeId: computeA.id,
    storageId: storage.id,
    attachmentId: attachmentA.id,
    url: `https://workspace.medopl.cn/w/ws-a/?token=share-a-${runId}`,
    access: { tokenStatus: "active" }
  };
  const workspaceB = {
    ...workspaceA,
    id: "ws-b",
    computeId: computeB.id,
    attachmentId: attachmentB.id,
    url: `https://workspace.medopl.cn/w/ws-b/?token=share-b-${runId}`
  };
  return { computeA, computeB, storage, attachmentA, attachmentB, workspaceA, workspaceB };
}

test("production persistence E2E recreates compute while retaining and verifying mounted storage", async () => {
  const requests = [];
  const kubeCalls = [];
  const runId = "persist-flow";
  const chain = resourceChain(runId);
  const responses = {
    "GET /api/production/readiness": { ready: true, failedChecks: [], checks: [] },
    "GET /api/runtime/readiness": { ready: true, missingEnv: [], missingTools: [] },
    "POST /api/auth/operator-login": { user: { accountId: "admin", role: "admin" }, csrfToken: "csrf-auth" },
    "POST /api/billing/topups": { id: "pi-e2e", balance: 2000 },
    "POST /api/compute-resources": chain.computeA,
    "POST /api/storage-volumes": chain.storage,
    "POST /api/storage-attachments": chain.attachmentA,
    "POST /api/workspaces": chain.workspaceA,
    "POST /api/workspaces/runtime-status": readyRuntimeStatus(chain.workspaceA),
    [`GET ${chain.workspaceA.url}`]: "<html>one-person-lab-app</html>",
    "POST /api/storage-attachments/detach": { ...chain.attachmentA, status: "detached" },
    "POST /api/compute-resources/destroy": { ...chain.computeA, status: "destroyed", billingStatus: "closed" },
    "POST /api/compute-resources#2": chain.computeB,
    "POST /api/storage-attachments#2": chain.attachmentB,
    "POST /api/workspaces#2": chain.workspaceB,
    "POST /api/workspaces/runtime-status#2": readyRuntimeStatus(chain.workspaceB),
    [`GET ${chain.workspaceB.url}`]: "<html>one-person-lab-app</html>",
    "POST /api/billing/request-usage": {
      id: "usage-request-e2e",
      workspaceId: chain.workspaceB.id,
      accountId: "pi-e2e",
      requestId: `production-persistence-e2e:${runId}`
    },
    "GET /api/state": {
      wallet: { accountId: "pi-e2e", balance: 1999, frozen: 10 },
      billingLedger: [
        { accountId: "pi-e2e", computeId: chain.computeA.id },
        { accountId: "pi-e2e", computeId: chain.computeB.id },
        { accountId: "pi-e2e", storageId: chain.storage.id },
        { accountId: "pi-e2e", attachmentId: chain.attachmentA.id },
        { accountId: "pi-e2e", attachmentId: chain.attachmentB.id },
        { accountId: "pi-e2e", workspaceId: chain.workspaceB.id, type: "request_debit" }
      ],
      resourceUsageLogs: [
        { accountId: "pi-e2e", computeId: chain.computeA.id },
        { accountId: "pi-e2e", computeId: chain.computeB.id },
        { accountId: "pi-e2e", storageId: chain.storage.id },
        { accountId: "pi-e2e", attachmentId: chain.attachmentA.id },
        { accountId: "pi-e2e", attachmentId: chain.attachmentB.id }
      ],
      requestUsageLogs: [
        { accountId: "pi-e2e", workspaceId: chain.workspaceB.id, requestId: `production-persistence-e2e:${runId}` }
      ]
    },
    "POST /api/storage-attachments/detach#2": { ...chain.attachmentB, status: "detached" },
    "POST /api/compute-resources/destroy#2": { ...chain.computeB, status: "destroyed", billingStatus: "closed" },
    "POST /api/storage-volumes/destroy": { ...chain.storage, status: "destroyed", billingStatus: "closed" }
  };
  const callCounts = new Map();

  const fetchImpl = async (url, options = {}) => {
    const parsed = new URL(String(url));
    const method = options.method || "GET";
    let key = parsed.origin === "https://console.oplcloud.cn" ? `${method} ${parsed.pathname}` : `${method} ${String(url)}`;
    if (["POST /api/compute-resources", "POST /api/storage-attachments", "POST /api/workspaces", "POST /api/workspaces/runtime-status", "POST /api/storage-attachments/detach", "POST /api/compute-resources/destroy"].includes(key)) {
      const count = (callCounts.get(key) || 0) + 1;
      callCounts.set(key, count);
      if (count > 1) key = `${key}#${count}`;
    }
    requests.push({
      key,
      body: options.body ? JSON.parse(options.body) : null,
      csrf: options.headers?.["x-opl-csrf-token"] || ""
    });
    const payload = responses[key];
    if (typeof payload === "string") return htmlResponse(payload);
    if (payload) {
      if (key === "POST /api/auth/operator-login") {
        return jsonResponse(payload, 200, new Headers({
          "content-type": "application/json",
          "set-cookie": "opl_console_session=operator-session; Path=/; HttpOnly; SameSite=Lax",
          "x-opl-csrf-token": "csrf-auth"
        }));
      }
      return jsonResponse(payload);
    }
    throw new Error(`unexpected_request:${key}`);
  };

  const kube = {
    async waitForComputeNodes(compute) {
      kubeCalls.push(["waitForComputeNodes", compute.id]);
    },
    async waitForRuntimePod(workspace) {
      kubeCalls.push(["waitForRuntimePod", workspace.id, workspace.computeId]);
      return { podName: `pod-${workspace.computeId}` };
    },
    async writeFile({ podName, filePath, content }) {
      kubeCalls.push(["writeFile", podName, filePath, content]);
      return { sha256: createHash("sha256").update(content).digest("hex") };
    },
    async readFile({ podName, filePath }) {
      kubeCalls.push(["readFile", podName, filePath]);
      const content = `OPL Cloud persistence E2E ${runId}\n`;
      return { content, sha256: createHash("sha256").update(content).digest("hex") };
    }
  };

  const result = await verifyProductionPersistenceChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-e2e",
    runId,
    operatorToken: "operator-token",
    retryDelayMs: 0,
    workspaceUrlAttempts: 1,
    fetchImpl,
    kube
  });

  assert.equal(result.ok, true);
  assert.equal(result.storageId, chain.storage.id);
  assert.equal(result.firstComputeId, chain.computeA.id);
  assert.equal(result.secondComputeId, chain.computeB.id);
  assert.equal(result.file.sha256, createHash("sha256").update(`OPL Cloud persistence E2E ${runId}\n`).digest("hex"));
  assert.deepEqual(kubeCalls.map((call) => call.slice(0, 2)), [
    ["waitForComputeNodes", chain.computeA.id],
    ["waitForRuntimePod", chain.workspaceA.id],
    ["writeFile", "pod-compute-a"],
    ["waitForComputeNodes", chain.computeB.id],
    ["waitForRuntimePod", chain.workspaceB.id],
    ["readFile", "pod-compute-b"]
  ]);
  assert.deepEqual(requests.map((request) => request.key), [
    "GET /api/production/readiness",
    "GET /api/runtime/readiness",
    "POST /api/auth/operator-login",
    "POST /api/billing/topups",
    "POST /api/compute-resources",
    "POST /api/storage-volumes",
    "POST /api/storage-attachments",
    "POST /api/workspaces",
    "POST /api/workspaces/runtime-status",
    `GET ${chain.workspaceA.url}`,
    "POST /api/storage-attachments/detach",
    "POST /api/compute-resources/destroy",
    "POST /api/compute-resources#2",
    "POST /api/storage-attachments#2",
    "POST /api/workspaces#2",
    "POST /api/workspaces/runtime-status#2",
    `GET ${chain.workspaceB.url}`,
    "POST /api/billing/request-usage",
    "GET /api/state",
    "POST /api/storage-attachments/detach#2",
    "POST /api/compute-resources/destroy#2",
    "POST /api/storage-volumes/destroy"
  ]);
  for (const request of requests.filter((item) => item.key.startsWith("POST /api/") && item.key !== "POST /api/auth/operator-login")) {
    assert.equal(request.csrf, "csrf-auth");
  }
});
