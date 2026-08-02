import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import * as productionLiveQa from "../../tools/production-live-qa.ts";

import {
  LIVE_QA_CONFIRMATION,
  runProductionLiveQaCli,
  verifyProductionLiveQa
} from "../../tools/production-live-qa.ts";

const fixedSlotDescriptor = {
  id: "verification-slot-basic-01",
  customerProduct: false,
  instanceType: "SA5.MEDIUM4",
  server: "2c4g",
  cpu: 2,
  memoryGb: 4,
  cbsGb: 10,
  chargeType: "PREPAID",
  periodMonths: 1,
  renewFlag: "NOTIFY_AND_MANUAL_RENEW"
};
const BASIC_ACCOUNT_ID = "acct-verification-slot-basic-01";
const ADMIN_ACCOUNT_ID = "acct-admin";
const ADMIN_USER_ID = "usr-admin";
const ADMIN_EMAIL = "admin@medopl.cn";
const ADMIN_PASSWORD = "existing-admin-password";
const ownerSeed = JSON.stringify([{
  id: "usr-verifier",
  email: "owner@example.com",
  password: "console-password",
  role: "owner",
  accountId: BASIC_ACCOUNT_ID,
  sub2apiUserId: 41
}]);
const mutationApprovalJson = JSON.stringify({
  approvalId: "approval-production-verification",
  expiresAt: "2099-07-19T00:00:00Z",
  accountIds: [BASIC_ACCOUNT_ID],
  workspaceIds: ["workspace-slot-1"],
  resourceIds: [fixedSlotDescriptor.id, "9"]
});

test("customer Basic canary orchestration stays inside production-live-qa", () => {
  assert.equal(typeof productionLiveQa.verifyProductionBasicCustomerCanary, "function");
});

test("Workspace identity diagnosis binds operator, customer, and the unique reserved Key without exposing secrets", async () => {
  const accountId = "acct-f947b18f844e42b3c0";
  const ownerUserId = "usr-huangrende";
  const workspaceId = "ws-4357c2c5b3ea1a344c";
  const launchOperationId = "workspace-launch-f0375970d7678d0a3e";
  const workspaceApiKeyId = "132";
  const customerEmail = "customer@example.com";
  const calls = [];
  let launchWorkspaceApiKeyId = workspaceApiKeyId;
  let launchStatus = "compute_claim_pending";
  let launchErrorCode = "";
  let duplicateLaunch = false;
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = String(init.method || "GET").toUpperCase();
    calls.push([method, url.pathname, url.search]);
    if (url.pathname === "/api/auth/login") {
      const body = JSON.parse(String(init.body || "{}"));
      if (body.email === ADMIN_EMAIL) {
        return json({ user: { accountId: ADMIN_ACCOUNT_ID, role: "admin" } }, 200, {
          "set-cookie": "opl_session=admin; Path=/; HttpOnly",
          "x-opl-csrf-token": "admin-csrf"
        });
      }
      if (body.email === customerEmail) {
        return json({ user: { accountId, role: "owner" } }, 200, {
          "set-cookie": "opl_session=customer; Path=/; HttpOnly",
          "x-opl-csrf-token": "customer-csrf"
        });
      }
    }
    if (url.pathname === "/api/auth/me") {
      return source({ consoleUserId: ownerUserId, accountId, sub2apiUserId: 10, email: customerEmail, role: "owner", status: "active" });
    }
    if (url.pathname === "/api/operator/accounts") {
      const page = Number(url.searchParams.get("page"));
      const pageSize = Number(url.searchParams.get("pageSize"));
      const filler = Array.from({ length: 50 }, (_, index) => ({
        accountId: `acct-filler-${index + 1}`,
        consoleUserId: `usr-filler-${index + 1}`,
        sub2apiUserId: String(index + 100),
        email: `filler-${index + 1}@example.com`,
        role: "owner",
        status: "active"
      }));
      return source({
        items: page === 1 ? filler : [{ accountId, consoleUserId: ownerUserId, sub2apiUserId: "10", email: customerEmail, role: "owner", status: "active" }],
        total: 51,
        page,
        pageSize
      }, "control-plane+sub2api");
    }
    if (url.pathname === "/api/workspace-launches") {
      const launch = {
        operationId: launchOperationId,
        accountId,
        workspaceId,
        status: launchStatus,
        phase: "compute_claim_pending",
        errorCode: launchErrorCode,
        workspaceApiKeyId: launchWorkspaceApiKeyId
      };
      return json(duplicateLaunch ? [launch, { ...launch, operationId: "workspace-launch-a0375970d7678d0a3e" }] : [launch]);
    }
    if (url.pathname === "/api/gateway/keys") {
      const page = Number(url.searchParams.get("page"));
      const pageSize = Number(url.searchParams.get("pageSize"));
      const items = page === 1
        ? Array.from({ length: 20 }, (_, index) => ({ id: String(index + 1), kind: "general", name: `general-${index + 1}`, status: "active" }))
        : [{ id: workspaceApiKeyId, kind: "workspace", name: "opl-workspace-cad539244292", status: "active" }];
      return source({ items, total: 21, page, pageSize, pages: 2 });
    }
    return json({ error: "not_found" }, 404);
  };

  const result = await productionLiveQa.diagnoseWorkspaceIdentity({
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    customerEmail,
    customerPassword: "customer-password",
    accountId,
    workspaceId,
    fetchImpl,
    now: new Date("2026-08-01T01:02:03Z")
  });

  assert.deepEqual(result, {
    schemaVersion: 1,
    operationMode: "workspace_identity_diagnose",
    status: "proven",
    identity: {
      accountId,
      ownerUserId,
      workspaceId,
      workspaceApiKeyId,
      sub2apiUserId: "10",
      customerEmailSha256: createHash("sha256").update(customerEmail).digest("hex"),
      workspaceKey: { id: workspaceApiKeyId, kind: "workspace", name: "opl-workspace-cad539244292", status: "active" }
    },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    verifiedAt: "2026-08-01T01:02:03.000Z"
  });
  assert.deepEqual(calls, [
    ["POST", "/api/auth/login", ""],
    ["GET", "/api/operator/accounts", "?page=1&pageSize=50"],
    ["GET", "/api/operator/accounts", "?page=2&pageSize=50"],
    ["POST", "/api/auth/login", ""],
    ["GET", "/api/auth/me", ""],
    ["GET", "/api/workspace-launches", ""],
    ["GET", "/api/gateway/keys", "?page=1&pageSize=20"],
    ["GET", "/api/gateway/keys", "?page=2&pageSize=20"]
  ]);
  assert.doesNotMatch(JSON.stringify(result), /customer@example\.com|password|csrf|cookie|token|maskedValue|\"value\"/i);
  assert.equal(calls.every(([method]) => ["GET", "POST"].includes(method)), true);
  assert.equal(calls.filter(([method, path]) => method === "POST" && path !== "/api/auth/login").length, 0);

  launchStatus = "manual_review";
  launchErrorCode = "workspace_compute_claim_identity_mismatch";
  const manualReviewResult = await productionLiveQa.diagnoseWorkspaceIdentity({
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    customerEmail,
    customerPassword: "customer-password",
    accountId,
    workspaceId,
    fetchImpl
  });
  assert.equal(manualReviewResult.status, "proven");
  assert.equal(manualReviewResult.identity.workspaceApiKeyId, workspaceApiKeyId);

  launchWorkspaceApiKeyId = "133";
  await assert.rejects(productionLiveQa.diagnoseWorkspaceIdentity({
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    customerEmail,
    customerPassword: "customer-password",
    accountId,
    workspaceId,
    fetchImpl
  }), /workspace_identity_workspace_key_mismatch/);

  launchWorkspaceApiKeyId = workspaceApiKeyId;
  launchStatus = "preparing";
  launchErrorCode = "";
  await assert.rejects(productionLiveQa.diagnoseWorkspaceIdentity({
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    customerEmail,
    customerPassword: "customer-password",
    accountId,
    workspaceId,
    fetchImpl
  }), /workspace_identity_launch_binding_mismatch/);

  launchStatus = "manual_review";
  launchErrorCode = "workspace_compute_claim_identity_mismatch";
  duplicateLaunch = true;
  await assert.rejects(productionLiveQa.diagnoseWorkspaceIdentity({
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    customerEmail,
    customerPassword: "customer-password",
    accountId,
    workspaceId,
    fetchImpl
  }), /workspace_identity_launch_binding_mismatch/);
});

test("Workspace identity CLI rejects write-capable flags before network access", async () => {
  let fetchCalls = 0;
  let stderr = "";
  const code = await runProductionLiveQaCli({
    argv: ["--workspace-identity-diagnose", "--allow-model-write"],
    env: {},
    fetchImpl: async () => {
      fetchCalls += 1;
      throw new Error("network_not_expected");
    },
    stdout: { write() {} },
    stderr: { write(value) { stderr += value; } }
  });
  assert.equal(code, 1);
  assert.equal(fetchCalls, 0);
  assert.match(stderr, /workspace_identity_diagnose_conflict/);
});

test("customer Basic canary Pod evidence uses one read-only kubectl get", async () => {
  assert.equal(typeof productionLiveQa.readBasicCanaryRuntimePodEvidence, "function");
  const calls = [];
  const result = await productionLiveQa.readBasicCanaryRuntimePodEvidence({
    workspaceId: BASIC_CANARY_WORKSPACE_ID,
    expectedDigest: BASIC_CANARY_DIGEST,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    execFileImpl: async (command, args) => {
      calls.push({ command, args });
      return { stdout: JSON.stringify({
        kind: "List",
        items: [{
          metadata: {
            name: "runtime-basic-canary-abc",
            labels: { "oplcloud.cn/workspace-id": BASIC_CANARY_WORKSPACE_ID },
            ownerReferences: [{ apiVersion: "apps/v1", kind: "ReplicaSet", name: "runtime-basic-canary-rs", uid: "rs-uid", controller: true }]
          },
          spec: {
            nodeName: "10.66.1.18",
            containers: [{ name: "workspace", resources: { limits: { cpu: "2", memory: "4Gi" } } }]
          },
          status: {
            phase: "Running",
            conditions: [{ type: "Ready", status: "True" }],
            containerStatuses: [{ name: "workspace", ready: true, imageID: `containerd://${BASIC_CANARY_DIGEST}` }]
          }
        }]
      }) };
    }
  });
  assert.deepEqual(calls, [{
    command: "kubectl",
    args: ["--kubeconfig", "/run/secrets/kubeconfig", "-n", "opl-cloud", "get", "pods", "-l", `oplcloud.cn/workspace-id=${BASIC_CANARY_WORKSPACE_ID}`, "-o", "json"]
  }]);
  assert.deepEqual(result, {
    podName: "runtime-basic-canary-abc",
    nodeName: "10.66.1.18",
    containerName: "workspace",
    ready: true,
    imageID: `containerd://${BASIC_CANARY_DIGEST}`,
    resources: { cpu: 2, memoryGb: 4 },
    ownerReference: { kind: "ReplicaSet", name: "runtime-basic-canary-rs", uid: "rs-uid" }
  });
});

test("customer Basic canary Pod evidence rejects empty node, multiple current Pods, and historical-only Pods", async () => {
  const pod = () => ({
    metadata: {
      name: "runtime-basic-canary-abc",
      labels: { "oplcloud.cn/workspace-id": BASIC_CANARY_WORKSPACE_ID },
      ownerReferences: [{ apiVersion: "apps/v1", kind: "ReplicaSet", name: "runtime-basic-canary-rs", uid: "rs-uid", controller: true }]
    },
    spec: { nodeName: "10.66.1.18", containers: [{ name: "workspace", resources: { limits: { cpu: "2", memory: "4Gi" } } }] },
    status: {
      phase: "Running",
      conditions: [{ type: "Ready", status: "True" }],
      containerStatuses: [{ name: "workspace", ready: true, imageID: `containerd://${BASIC_CANARY_DIGEST}` }]
    }
  });
  for (const [name, items] of [
    ["empty node", [{ ...pod(), spec: { ...pod().spec, nodeName: "" } }]],
    ["multiple current Pods", [pod(), { ...pod(), metadata: { ...pod().metadata, name: "runtime-basic-canary-def" } }]],
    ["historical only", [{ ...pod(), metadata: { ...pod().metadata, deletionTimestamp: "2026-07-26T00:00:00Z" } }]]
  ]) {
    await assert.rejects(() => productionLiveQa.readBasicCanaryRuntimePodEvidence({
      workspaceId: BASIC_CANARY_WORKSPACE_ID,
      expectedDigest: BASIC_CANARY_DIGEST,
      kubeconfigPath: "/run/secrets/kubeconfig",
      namespace: "opl-cloud",
      execFileImpl: async () => ({ stdout: JSON.stringify({ kind: "List", items }) })
    }), /production_basic_canary_runtime_pod_invalid/, name);
  }
});

function json(payload, status = 200, headers = {}) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json", ...headers }
  });
}

function source(payload, sourceName = "sub2api", status = "available", headers = {}) {
  return json({ source: sourceName, status, available: true, fetchedAt: new Date().toISOString(), data: payload }, 200, {
    "cache-control": "private, no-store",
    ...headers
  });
}

function nestedSource(payload, sourceName) {
  return { source: sourceName, status: "available", available: true, fetchedAt: new Date().toISOString(), data: payload };
}

class FakeEmitter {
  handlers = new Map();

  on(name, handler) {
    const handlers = this.handlers.get(name) || [];
    handlers.push(handler);
    this.handlers.set(name, handlers);
  }

  emit(name, payload) {
    for (const handler of this.handlers.get(name) || []) handler(payload);
  }
}

function browserFactory(state, { frames = true, responseSuffix = "" } = {}) {
  return async () => {
    const cdp = new FakeEmitter();
    cdp.send = async () => {};
    const page = new FakeEmitter();
    const socket = new FakeEmitter();
    socket.url = () => "wss://workspace.medopl.cn/ws";
    let qaToken = "";

    const assistant = {
      waitFor: async () => {},
      textContent: async () => `${qaToken}${responseSuffix}`
    };
    page.locator = (selector) => {
      if (selector === "[data-testid='guid-input']") {
        return {
          waitFor: async () => {},
          fill: async (value) => { qaToken = value.match(/OPL_QA_[A-Z0-9_]+/)?.[0] || ""; }
        };
      }
      if (selector === "[data-testid='guid-send-btn']") {
        return { click: async () => { state.modelRequests += 1; } };
      }
      return { filter: () => ({ last: () => assistant }) };
    };
    page.goto = async () => ({ ok: () => true, status: () => 200 });
    page.evaluate = async () => ({ status: 200, payload: { success: true, user: { username: "opl" } } });
    page.reload = async () => {
      page.emit("websocket", socket);
      cdp.emit("Network.webSocketCreated", { requestId: "ws-1", url: socket.url() });
      cdp.emit("Network.webSocketHandshakeResponseReceived", {
        requestId: "ws-1",
        response: { status: 101, url: socket.url() }
      });
      if (frames) {
        socket.emit("framereceived", { payload: "ping" });
        socket.emit("framesent", { payload: "pong" });
      }
    };
    page.waitForURL = async () => {};

    const apiResponse = (payload, status = 200) => ({
      ok: () => status >= 200 && status < 300,
      status: () => status,
      json: async () => payload
    });
    const context = {
      request: {
        post: async (url, options) => {
          assert.equal(new URL(url).pathname, "/login");
          assert.deepEqual(options.data, { username: "opl", password: "workspace-password", remember: true });
          return apiResponse({ success: true, user: { username: "opl" } });
        },
        get: async () => { throw new Error("auth_user_must_be_checked_in_page_context"); }
      },
      newPage: async () => page,
      newCDPSession: async () => cdp,
      close: async () => {}
    };
    return { newContext: async () => context, close: async () => {} };
  };
}

function readOnlyBrowserFactory(viewports) {
  return async () => ({
    newContext: async ({ viewport }) => {
      viewports.push(viewport);
      return {
        newPage: async () => ({
          goto: async () => ({ ok: () => true, status: () => 200 }),
          locator: () => ({ innerText: async () => "OPL Cloud Console" })
        }),
        close: async () => {}
      };
    },
    close: async () => {}
  });
}

const BASIC_CANARY_DIGEST = `sha256:${"a".repeat(64)}`;
const BASIC_CANARY_CLOUD_DIGEST = `sha256:${"b".repeat(64)}`;
const BASIC_CANARY_CLOUD_IMAGE = `uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@${BASIC_CANARY_CLOUD_DIGEST}`;
const BASIC_CANARY_MERGED_SHA = "c".repeat(40);
const BASIC_CANARY_APPROVAL_ID = "approval-production-basic-canary";
const BASIC_CANARY_CONFIRMATION = "I_UNDERSTAND_THIS_PROVISIONS_ONE_REAL_BASIC_WORKSPACE_AND_SENDS_ONE_MODEL_REQUEST";
const BASIC_CANARY_CUSTOMER_EMAIL = "basic-canary@example.com";
const BASIC_CANARY_CUSTOMER_PASSWORD = "customer-password";
const BASIC_CANARY_ACCOUNT_ID = "acct-31c43adae1a4dc1805";
const BASIC_CANARY_WALLET_OPERATION_ID = "wallet-adjustment-ca5b714ed4b0ae2451";
const BASIC_CANARY_LAUNCH_OPERATION_ID = "workspace-launch-295abada3ddad29c7d";
const BASIC_CANARY_WORKSPACE_ID = "ws-f55d9cdbcf57afb726";
const BASIC_CANARY_KEY_ID = "91";
const BASIC_CANARY_LAUNCH_KEY = "workspace-launch:prod-basic-canary-20260726-01";
const BASIC_CANARY_RESOLVED_INSTANCE_TYPE = "S5.MEDIUM4";

function stableCanaryId(...parts) {
  const hash = createHash("sha1");
  for (const part of parts) {
    hash.update(String(part));
    hash.update(Buffer.from([0]));
  }
  return hash.digest("hex");
}

function basicWorkspaceKeyName(workspaceId = BASIC_CANARY_WORKSPACE_ID) {
  return `opl-workspace-${stableCanaryId(workspaceId).slice(0, 12)}`;
}

function generalKey(id, name = `general-${id}`, status = "active") {
  return { id: String(id), kind: "general", name, status };
}

function workspaceKey(id = BASIC_CANARY_KEY_ID, { name = basicWorkspaceKeyName(), status = "active" } = {}) {
  return { id: String(id), kind: "workspace", name, status };
}

function basicCanaryApprovalJson({ rechargeUsdMicros = "100000000" } = {}) {
  return JSON.stringify({
    approvalId: BASIC_CANARY_APPROVAL_ID,
    expiresAt: "2099-07-26T00:00:00Z",
    customer: { email: BASIC_CANARY_CUSTOMER_EMAIL, name: "Basic Canary Customer" },
    rechargeUsdMicros,
    idempotencyKeys: {
      accountProvision: "account-provision:prod-basic-canary-20260726-01",
      walletAdjustment: "wallet-adjustment:prod-basic-canary-20260726-01",
      workspaceLaunch: BASIC_CANARY_LAUNCH_KEY
    },
    launch: { name: "Basic Canary 2026-07-26", packageId: "basic", sizeGb: 10, autoRenew: false },
    expected: {
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      nodePoolId: "np-basic",
      resolvedInstanceType: BASIC_CANARY_RESOLVED_INSTANCE_TYPE,
      workspaceImageDigest: BASIC_CANARY_DIGEST,
      model: "gpt-5.5"
    }
  });
}

function recoveredPrechargeBasicCanaryApprovalJson({
  prechargeOperationId = BASIC_CANARY_WALLET_OPERATION_ID,
  rechargeUsdMicros = "60000000",
  launchOperationId = BASIC_CANARY_LAUNCH_OPERATION_ID,
  workspaceId = BASIC_CANARY_WORKSPACE_ID
} = {}) {
  return JSON.stringify({
    approvalId: BASIC_CANARY_APPROVAL_ID,
    expiresAt: "2099-07-26T00:00:00Z",
    fundingMode: "operator_precharge_recovery",
    customer: { email: BASIC_CANARY_CUSTOMER_EMAIL, accountId: BASIC_CANARY_ACCOUNT_ID },
    prechargeOperationId,
    rechargeUsdMicros,
    idempotencyKeys: { workspaceLaunch: BASIC_CANARY_LAUNCH_KEY },
    launch: { name: "Basic Canary 2026-07-26", packageId: "basic", sizeGb: 10, autoRenew: false },
    expected: {
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      nodePoolId: "np-basic",
      resolvedInstanceType: BASIC_CANARY_RESOLVED_INSTANCE_TYPE,
      workspaceImageDigest: BASIC_CANARY_DIGEST,
      model: "gpt-5.5",
      launchOperationId,
      workspaceId
    }
  });
}

function cloudRevisionFixture({ component = "", failure = "", remoteMainSha = BASIC_CANARY_MERGED_SHA } = {}) {
  const definitions = [
    ["opl-cloud-control-plane", "control-plane", "101"],
    ["opl-cloud-fabric", "fabric", "202"],
    ["opl-cloud-ledger", "ledger", "303"]
  ];
  const deployments = definitions.map(([name, container, revision]) => ({
    metadata: { name, uid: `${name}-uid`, generation: 8, annotations: { "deployment.kubernetes.io/revision": revision } },
    spec: { replicas: 1, template: { spec: { containers: [{ name: container, image: component === container && failure === "deployment_digest" ? `${BASIC_CANARY_CLOUD_IMAGE}-wrong` : BASIC_CANARY_CLOUD_IMAGE }] } } },
    status: { observedGeneration: 8, updatedReplicas: 1, readyReplicas: 1, availableReplicas: 1, unavailableReplicas: 0 }
  }));
  const replicaSets = definitions.map(([name, container, revision]) => ({
    metadata: {
      name: `${name}-rs`,
      uid: `${name}-rs-uid`,
      annotations: { "deployment.kubernetes.io/revision": component === container && failure === "replicaset_revision" ? `${Number(revision) - 1}` : revision },
      ownerReferences: [{ kind: "Deployment", name, uid: `${name}-uid`, controller: true }]
    },
    spec: { template: { spec: { containers: [{ name: container, image: BASIC_CANARY_CLOUD_IMAGE }] } } }
  }));
  const pods = definitions.map(([name, container]) => ({
    metadata: {
      name: `${name}-pod`,
      uid: `${name}-pod-uid`,
      ownerReferences: [{ kind: "ReplicaSet", name: `${name}-rs`, uid: component === container && failure === "pod_owner" ? "wrong-rs-uid" : `${name}-rs-uid`, controller: true }]
    },
    status: {
      phase: "Running",
      conditions: [{ type: "Ready", status: "True" }],
      containerStatuses: [{
        name: container,
        ready: true,
        imageID: `containerd://${BASIC_CANARY_CLOUD_IMAGE}${component === container && failure === "pod_digest" ? "-wrong" : ""}`
      }]
    }
  }));
  replicaSets.push({
    metadata: {
      name: "opl-cloud-fabric-rs-historical",
      uid: "opl-cloud-fabric-rs-historical-uid",
      annotations: { "deployment.kubernetes.io/revision": "201" },
      ownerReferences: [{ kind: "Deployment", name: "opl-cloud-fabric", uid: "opl-cloud-fabric-uid", controller: true }]
    },
    spec: { template: { spec: { containers: [{ name: "fabric", image: `uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:${"d".repeat(64)}` }] } } }
  });
  pods.push({
    metadata: {
      name: "opl-cloud-fabric-historical-evicted",
      uid: "opl-cloud-fabric-historical-evicted-uid",
      deletionTimestamp: "2026-07-25T23:59:00Z",
      ownerReferences: [{ kind: "ReplicaSet", name: "opl-cloud-fabric-rs-historical", uid: "opl-cloud-fabric-rs-historical-uid", controller: true }]
    },
    status: {
      phase: "Failed",
      reason: "Evicted",
      conditions: [{ type: "Ready", status: "False" }],
      containerStatuses: []
    }
  });
  const calls = [];
  const execFileImpl = async (command, args) => {
    calls.push({ command, args });
    if (command === "git" && args.join(" ") === "rev-parse HEAD") return { stdout: `${BASIC_CANARY_MERGED_SHA}\n`, stderr: "" };
    if (command === "git" && args.join(" ") === "rev-parse refs/remotes/origin/main") return { stdout: `${BASIC_CANARY_MERGED_SHA}\n`, stderr: "" };
    if (command === "git" && args.join(" ") === "ls-remote --exit-code origin refs/heads/main") {
      return { stdout: `${remoteMainSha}\trefs/heads/main\n`, stderr: "" };
    }
    if (command !== "kubectl") throw new Error(`unexpected_command:${command}`);
    const resource = args[args.indexOf("get") + 1];
    if (resource === "configmap") return { stdout: JSON.stringify({ metadata: { name: "opl-cloud-config" }, data: { OPL_CLOUD_IMAGE: BASIC_CANARY_CLOUD_IMAGE } }), stderr: "" };
    if (resource === "deployments") return { stdout: JSON.stringify({ kind: "List", items: deployments }), stderr: "" };
    if (resource === "replicasets") return { stdout: JSON.stringify({ kind: "List", items: replicaSets }), stderr: "" };
    if (resource === "pods") return { stdout: JSON.stringify({ kind: "List", items: pods }), stderr: "" };
    throw new Error(`unexpected_resource:${resource}`);
  };
  return { calls, execFileImpl };
}

test("customer Basic canary reads one exact immutable Cloud revision before business writes", async () => {
  const fixture = cloudRevisionFixture();
  const result = await productionLiveQa.readBasicCanaryCloudRevisionEvidence({
    expectedMergedSha: BASIC_CANARY_MERGED_SHA,
    expectedCloudDigest: BASIC_CANARY_CLOUD_DIGEST,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    execFileImpl: fixture.execFileImpl
  });

  assert.equal(result.mergedSha, BASIC_CANARY_MERGED_SHA);
  assert.equal(result.cloudDigest, BASIC_CANARY_CLOUD_DIGEST);
  assert.deepEqual(Object.keys(result.services).sort(), ["controlPlane", "fabric", "ledger"]);
  assert.deepEqual(Object.values(result.services).map((service) => service.revision), ["101", "202", "303"]);
  assert.equal(result.services.fabric.replicaSet, "opl-cloud-fabric-rs");
  assert.equal(fixture.calls.some(({ command, args }) => command === "git" && args.join(" ") === "ls-remote --exit-code origin refs/heads/main"), true);
  assert.equal(fixture.calls.every(({ command, args }) => command === "git" && ["rev-parse", "ls-remote"].includes(args[0]) || command === "kubectl" && args.includes("get")), true);
});

test("customer Basic canary Cloud revision evidence fails closed on every service digest, revision, and owner mismatch", async () => {
  for (const component of ["control-plane", "fabric", "ledger"]) {
    for (const failure of ["deployment_digest", "replicaset_revision", "pod_digest", "pod_owner"]) {
      const fixture = cloudRevisionFixture({ component, failure });
      await assert.rejects(() => productionLiveQa.readBasicCanaryCloudRevisionEvidence({
        expectedMergedSha: BASIC_CANARY_MERGED_SHA,
        expectedCloudDigest: BASIC_CANARY_CLOUD_DIGEST,
        kubeconfigPath: "/run/secrets/kubeconfig",
        namespace: "opl-cloud",
        execFileImpl: fixture.execFileImpl
      }), /production_basic_canary_cloud_revision_invalid/, `${component}:${failure}`);
    }
  }
});

test("customer Basic canary Cloud revision evidence fails closed when live origin main moves", async () => {
  const fixture = cloudRevisionFixture({ remoteMainSha: "d".repeat(40) });
  await assert.rejects(() => productionLiveQa.readBasicCanaryCloudRevisionEvidence({
    expectedMergedSha: BASIC_CANARY_MERGED_SHA,
    expectedCloudDigest: BASIC_CANARY_CLOUD_DIGEST,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    execFileImpl: fixture.execFileImpl
  }), /production_basic_canary_cloud_revision_invalid/);
});

function basicCanaryFixture({
  terminalStatus = "succeeded",
  truthStorageProviderId = "disk-basic-canary",
  basicCpu = 2,
  basicMemoryGb = 4,
  basicDiskGb = 10,
  podNodeName = "10.66.1.18",
  allocationCpu = 2,
  allocationMemoryGb = 4,
  initialProvisioned = false,
  initialRecharged = false,
  initialWalletUsdMicros = null,
  rechargeUsdMicros = 100_000_000,
  walletAdjustmentPhase = "complete",
  walletAdjustmentOverrides = {},
  walletAdjustmentPostPayload = null,
  initialLaunchStatus = "",
  cloudRevisionError = "",
  loseResponseAfter = "",
  existingGeneralKeys = [],
  existingWorkspaceKeys = [],
  workspaceKeysAfter = [workspaceKey()],
  generalKeySpendBeforeLaunchUsdMicros = 0,
  generalKeySpendAfterModelUsdMicros = 0,
  walletPayload = null,
  quoteTotalUsdMicros = 52_580_000,
  quoteComputeUsdMicros = 50_000_000,
  quoteStorageUsdMicros = 2_580_000,
  quoteStorageSizeGb = 10,
  launchTotalUsdMicros = quoteTotalUsdMicros,
  receiptTotalUsdMicros = quoteTotalUsdMicros,
  receiptComputeUsdMicros = quoteComputeUsdMicros,
  receiptStorageUsdMicros = quoteStorageUsdMicros
} = {}) {
  const calls = [];
  const state = {
    provisionPosts: 0,
    rechargePosts: 0,
    launchPosts: 0,
    workspacePurchaseDebits: 0,
    tencentCvmPurchases: 0,
    tencentCbsPurchases: 0,
    launchPolls: 0,
    modelRequests: 0,
    provisioned: initialProvisioned,
    launched: Boolean(initialLaunchStatus),
    recharged: initialRecharged,
    initialLaunchStatus,
    podReads: 0,
    cloudRevisionReads: 0,
    lostResponses: new Set()
  };
  const periodStart = "2026-07-26T00:00:00Z";
  const paidThrough = "2026-08-26T00:00:00Z";
  const receipt = {
    receiptId: "receipt-basic-canary",
    type: "billing.workspace_purchased.v1",
    status: "completed",
    workspaceId: BASIC_CANARY_WORKSPACE_ID,
    createdAt: periodStart,
    resourceType: "workspace",
    resourceId: BASIC_CANARY_WORKSPACE_ID,
    priceVersion: "pilot-usd-2026-07-v1",
    currency: "USD",
    periodStart,
    paidThrough,
    totalUsdMicros: receiptTotalUsdMicros,
    components: {
      compute: { resourceType: "compute", resourceId: "ca-basic-canary", chargeUsdMicros: receiptComputeUsdMicros },
      storage: { resourceType: "storage", resourceId: "vol-basic-canary", sizeGb: 10, chargeUsdMicros: receiptStorageUsdMicros }
    },
    fulfillment: {
      computeAllocationId: "ca-basic-canary",
      storageId: "vol-basic-canary",
      attachmentId: "attachment-basic-canary",
      runtimeId: "runtime-basic-canary",
      workspaceApiKeyId: BASIC_CANARY_KEY_ID
    },
    chargeReference: "must-not-emit-redeem-code"
  };
  const usageRecord = {
    requestId: "req-basic-canary",
    apiKeyId: BASIC_CANARY_KEY_ID,
    model: "gpt-5.5",
    requestType: "sync",
    inboundEndpoint: "/v1/responses",
    inputTokens: 4,
    outputTokens: 3,
    cacheCreationTokens: 0,
    cacheReadTokens: 0,
    actualCostUsdMicros: 120
  };
  const launch = (status, phase = status) => ({
    operationId: BASIC_CANARY_LAUNCH_OPERATION_ID,
    accountId: BASIC_CANARY_ACCOUNT_ID,
    workspaceId: BASIC_CANARY_WORKSPACE_ID,
    status,
    phase,
    name: "Basic Canary 2026-07-26",
    packageId: "basic",
    sizeGb: 10,
    autoRenew: false,
    priceVersion: "pilot-usd-2026-07-v1",
    currency: "USD",
    totalChargeUsdMicros: launchTotalUsdMicros,
    computeAllocationId: "ca-basic-canary",
    storageId: "vol-basic-canary",
    attachmentId: status === "succeeded" ? "attachment-basic-canary" : "",
    workspaceApiKeyId: BASIC_CANARY_KEY_ID,
    workspaceKeyStatus: status === "succeeded" ? "active" : "pending",
    runtimeServiceName: status === "succeeded" ? "workspace-service-basic-canary" : "",
    url: status === "succeeded" ? `https://workspace.medopl.cn/w/${BASIC_CANARY_WORKSPACE_ID}/` : "",
    receiptId: status === "succeeded" ? receipt.receiptId : "",
    errorCode: status === "manual_review" ? "provider_result_unknown" : ""
  });
  const resourceFact = (type, providerId, packageOrSpec, status = "running") => ({
    resourceType: nestedSource(type, "fabric"),
    packageOrSpec: nestedSource(packageOrSpec, "fabric"),
    providerId: nestedSource(providerId, "fabric"),
    zone: nestedSource("na-siliconvalley-1", "fabric"),
    status: nestedSource(status, "fabric"),
    createdAt: nestedSource(periodStart, "fabric"),
    expiresAt: nestedSource(paidThrough, "fabric"),
    lastReadAt: nestedSource("2026-07-26T00:10:00Z", "fabric"),
    operationRef: nestedSource(`operation-${type}`, "control-plane"),
    receiptRef: nestedSource(receipt.receiptId, "ledger")
  });
  const controlPlanePage = (items) => source({ items, total: items.length, page: 1, pageSize: 20 }, "control-plane", items.length === 0 ? "empty" : "available");
  const gatewayPage = (items, page = 1, pageSize = 20, total = items.length) => {
    const pages = Math.max(1, Math.ceil(total / pageSize));
    return source({ items, total, page, pageSize, pages }, "sub2api", items.length === 0 ? "empty" : "available");
  };
  const keyItems = () => state.launched
    ? [...existingGeneralKeys, ...workspaceKeysAfter]
    : [...existingGeneralKeys, ...existingWorkspaceKeys];
  const operatorAccountPage = () => {
    const items = state.provisioned ? [{
      accountId: BASIC_CANARY_ACCOUNT_ID,
      consoleUserId: "usr-basic-canary",
      sub2apiUserId: "143",
      email: BASIC_CANARY_CUSTOMER_EMAIL,
      role: "owner",
      status: "active"
    }] : [];
    return source({ items, total: items.length, page: 1, pageSize: 50 }, "control-plane+sub2api", items.length === 0 ? "empty" : "available");
  };
  const rechargeUsd = `${Math.trunc(rechargeUsdMicros / 1_000_000)}.${String(rechargeUsdMicros % 1_000_000).padStart(6, "0")}`;
  const walletAdjustment = () => ({
    operationId: BASIC_CANARY_WALLET_OPERATION_ID,
    accountId: BASIC_CANARY_ACCOUNT_ID,
    kind: "recharge",
    amountUsd: rechargeUsd,
    reason: "production Basic customer canary precharge",
    status: "succeeded",
    phase: walletAdjustmentPhase,
    beforeBalance: nestedSource({ currency: "USD", usdMicros: "0" }, "sub2api"),
    afterBalance: nestedSource({ currency: "USD", usdMicros: String(rechargeUsdMicros) }, "sub2api"),
    ...walletAdjustmentOverrides
  });
  const walletBalance = () => {
    if (walletPayload) return source(walletPayload);
    const generalKeySpend = generalKeySpendBeforeLaunchUsdMicros + (state.modelRequests > 0 ? generalKeySpendAfterModelUsdMicros : 0);
    const openingBalance = initialWalletUsdMicros ?? (state.recharged ? rechargeUsdMicros : 0);
    const value = openingBalance - (state.launched ? launchTotalUsdMicros : 0) - state.modelRequests * usageRecord.actualCostUsdMicros - generalKeySpend;
    return source({ userId: "143", currency: "USD", usdMicros: String(value), status: "active" });
  };
  const fabricOperations = () => [{
    id: "fabric-op-compute",
    operationId: `${BASIC_CANARY_LAUNCH_OPERATION_ID}:compute`,
    action: "create_compute_allocation",
    resourceKind: "compute_allocation",
    resourceId: "ca-basic-canary",
    accountId: BASIC_CANARY_ACCOUNT_ID,
    workspaceId: BASIC_CANARY_WORKSPACE_ID,
    providerRequestId: "must-not-emit-provider-request-id",
    status: "succeeded",
    redactedProviderPayload: {
      allocationPlan: {
        poolId: "pool-basic-2c4g",
        packageId: "basic",
        nodePoolId: "np-basic",
        instanceType: BASIC_CANARY_RESOLVED_INSTANCE_TYPE,
        maxReplicas: 20,
        baselineReplicas: 7,
        targetReplicas: 8,
        beforeMachineNames: Array.from({ length: 7 }, (_, index) => `np-basic-old-${index + 1}`)
      }
    }
  }, ...["create_storage_volume", "create_storage_attachment", "upsert_gateway_secret", "create_workspace_runtime"].map((action) => ({
    id: `fabric-op-${action}`,
    operationId: `${BASIC_CANARY_LAUNCH_OPERATION_ID}:${action}`,
    action,
    resourceKind: action,
    resourceId: action === "create_storage_volume" ? "vol-basic-canary" : `${action}-basic-canary`,
    accountId: BASIC_CANARY_ACCOUNT_ID,
    workspaceId: BASIC_CANARY_WORKSPACE_ID,
    providerRequestId: `must-not-emit-${action}-request-id`,
    status: "succeeded",
    redactedProviderPayload: {}
  }))];

  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    const headers = new Headers(init.headers);
    calls.push({ host: url.host, method, path: url.pathname, search: url.search, body: init.body, headers });
    if (url.hostname === "fabric.opl-cloud.svc") {
      assert.equal(headers.get("authorization"), "Bearer internal-service-token");
      if (url.pathname === "/fabric/catalog") return json({
        schemaVersion: 1,
        owner: "OPL Fabric",
        workspacePackages: [
          { id: "basic", name: "Basic Workspace", computeProfileId: "cpu-basic", cpu: basicCpu, memoryGb: basicMemoryGb, diskGb: basicDiskGb, provider: "tencent-tke", available: true },
          { id: "pro", name: "Pro Workspace", computeProfileId: "cpu-pro", cpu: 8, memoryGb: 16, diskGb: 100, provider: "tencent-tke", available: true }
        ],
        storageClasses: [],
        ingressDomains: []
      });
      if (url.pathname === "/fabric/operations") return json(fabricOperations());
      if (url.pathname === "/fabric/compute-allocations/ca-basic-canary") return json({
        id: "ca-basic-canary", accountId: BASIC_CANARY_ACCOUNT_ID, workspaceId: BASIC_CANARY_WORKSPACE_ID, packageId: "basic",
        status: "running", provider: "tencent-tke", providerResourceId: "ins-basic-canary", nodePoolId: "np-basic",
        instanceId: "ins-basic-canary", cvmInstanceId: "ins-basic-canary", machineName: "np-basic-new-8", nodeName: "10.66.1.18",
        privateIp: "10.66.1.18", instanceType: BASIC_CANARY_RESOLVED_INSTANCE_TYPE, zone: "na-siliconvalley-1", cvmStatus: "RUNNING",
        chargeType: "PREPAID", renewFlag: "NOTIFY_AND_MANUAL_RENEW", deadline: paidThrough,
        providerData: { cpu: String(allocationCpu), memoryGb: String(allocationMemoryGb), instanceType: BASIC_CANARY_RESOLVED_INSTANCE_TYPE },
        providerRequestId: "must-not-emit-provider-request-id"
      });
      if (url.pathname === "/fabric/machine-ownerships/ca-basic-canary") return json({
        resourceId: "ca-basic-canary", accountId: BASIC_CANARY_ACCOUNT_ID, workspaceId: BASIC_CANARY_WORKSPACE_ID, packageId: "basic",
        nodePoolId: "np-basic", machineId: "np-basic-new-8", instanceId: "ins-basic-canary", nodeName: "10.66.1.18", status: "active",
        providerRequestId: "must-not-emit-provider-request-id"
      });
      if (url.pathname === "/fabric/monthly-provider-truth") return json({
        computeState: "ready", storageState: "ready",
        compute: { id: "ca-basic-canary", accountId: BASIC_CANARY_ACCOUNT_ID, workspaceId: BASIC_CANARY_WORKSPACE_ID, packageId: "basic", providerResourceId: "ins-basic-canary", nodePoolId: "np-basic", machineName: "np-basic-new-8", instanceId: "ins-basic-canary", instanceType: BASIC_CANARY_RESOLVED_INSTANCE_TYPE, zone: "na-siliconvalley-1", chargeType: "PREPAID", renewFlag: "NOTIFY_AND_MANUAL_RENEW", deadline: paidThrough, providerData: { cpu: String(allocationCpu), memoryGb: String(allocationMemoryGb), instanceType: BASIC_CANARY_RESOLVED_INSTANCE_TYPE } },
        storage: { id: "vol-basic-canary", accountId: BASIC_CANARY_ACCOUNT_ID, workspaceId: BASIC_CANARY_WORKSPACE_ID, providerResourceId: truthStorageProviderId, sizeGb: 10, zone: "na-siliconvalley-1", chargeType: "PREPAID", renewFlag: "NOTIFY_AND_MANUAL_RENEW", deadline: paidThrough }
      });
      return json({ error: "not_found" }, 404);
    }
    if (url.pathname === "/api/auth/login") {
      const body = JSON.parse(init.body);
      const admin = body.email === ADMIN_EMAIL;
      assert.equal(body.password, admin ? ADMIN_PASSWORD : BASIC_CANARY_CUSTOMER_PASSWORD);
      if (!admin && !state.provisioned) return json({ error: "invalid_credentials" }, 401);
      return json({ user: { id: admin ? ADMIN_USER_ID : "usr-basic-canary", accountId: admin ? ADMIN_ACCOUNT_ID : BASIC_CANARY_ACCOUNT_ID, role: admin ? "admin" : "owner" } }, 200, {
        "set-cookie": `opl_session=${admin ? "session-admin" : "session-customer"}; Path=/; HttpOnly`,
        "x-opl-csrf-token": admin ? "csrf-admin" : "csrf-customer"
      });
    }
    if (url.pathname === "/api/operator/accounts" && method === "POST") {
      state.provisionPosts += 1;
      assert.equal(headers.get("idempotency-key"), "account-provision:prod-basic-canary-20260726-01");
      assert.deepEqual(JSON.parse(init.body), { email: BASIC_CANARY_CUSTOMER_EMAIL, password: BASIC_CANARY_CUSTOMER_PASSWORD, name: "Basic Canary Customer" });
      state.provisioned = true;
      if (loseResponseAfter === "account" && !state.lostResponses.has("account")) {
        state.lostResponses.add("account");
        throw new Error("simulated_account_response_lost");
      }
      return json({ operationId: "account-provision-f6b06edcc89bf02427", accountId: BASIC_CANARY_ACCOUNT_ID, status: "succeeded" }, 201);
    }
    if (url.pathname === "/api/operator/accounts" && method === "GET") return operatorAccountPage();
    if (url.pathname === `/api/operator/accounts/${BASIC_CANARY_ACCOUNT_ID}/wallet-adjustments` && method === "POST") {
      state.rechargePosts += 1;
      assert.equal(headers.get("idempotency-key"), "wallet-adjustment:prod-basic-canary-20260726-01");
      assert.deepEqual(JSON.parse(init.body), { kind: "recharge", amountUsd: rechargeUsd, reason: "production Basic customer canary precharge", confirmationAccountId: BASIC_CANARY_ACCOUNT_ID });
      state.recharged = true;
      if (loseResponseAfter === "wallet" && !state.lostResponses.has("wallet")) {
        state.lostResponses.add("wallet");
        throw new Error("simulated_wallet_response_lost");
      }
      return json(walletAdjustmentPostPayload || walletAdjustment(), 201);
    }
    if (url.pathname === `/api/operator/wallet-adjustments/${BASIC_CANARY_WALLET_OPERATION_ID}` && method === "GET") {
      return state.recharged
        ? json(walletAdjustment())
        : json({ error: "not_found" }, 404);
    }
    if (url.pathname === "/api/auth/me") return source({ consoleUserId: "usr-basic-canary", accountId: BASIC_CANARY_ACCOUNT_ID, sub2apiUserId: 143, email: BASIC_CANARY_CUSTOMER_EMAIL, role: "owner", status: "active" });
    if (url.pathname === "/api/gateway/wallet") return walletBalance();
    if (url.pathname === "/api/operator/reconciliation") return source({ items: [], total: 0, page: 1, pageSize: 20 }, "control-plane", "empty");
    if (url.pathname === "/api/pricing/preview") return json({
      resourceType: "workspace",
      priceVersion: "pilot-usd-2026-07-v1",
      packageId: "basic",
      currency: "USD",
      displayCurrency: "USD",
      billingUnit: "calendar_month",
      compute: {
        resourceType: "compute",
        priceVersion: "pilot-usd-2026-07-v1",
        packageId: "basic",
        currency: "USD",
        displayCurrency: "USD",
        billingUnit: "calendar_month",
        chargeUsdMicros: quoteComputeUsdMicros,
        priceSnapshot: {
          resourceType: "compute",
          priceVersion: "pilot-usd-2026-07-v1",
          packageId: "basic",
          currency: "USD",
          displayCurrency: "USD",
          billingUnit: "calendar_month",
          chargeUsdMicros: quoteComputeUsdMicros
        }
      },
      storage: {
        resourceType: "storage",
        priceVersion: "pilot-usd-2026-07-v1",
        packageId: "basic",
        currency: "USD",
        displayCurrency: "USD",
        billingUnit: "calendar_month",
        chargeUsdMicros: quoteStorageUsdMicros,
        priceSnapshot: {
          resourceType: "storage",
          priceVersion: "pilot-usd-2026-07-v1",
          packageId: "basic",
          sizeGb: quoteStorageSizeGb,
          currency: "USD",
          displayCurrency: "USD",
          billingUnit: "calendar_month",
          chargeUsdMicros: quoteStorageUsdMicros
        }
      },
      totalChargeUsdMicros: quoteTotalUsdMicros
    });
    if (url.pathname === "/api/workspaces") return controlPlanePage(state.launched ? [{ id: BASIC_CANARY_WORKSPACE_ID, name: "Basic Canary 2026-07-26", packageId: "basic", state: "active", url: `https://workspace.medopl.cn/w/${BASIC_CANARY_WORKSPACE_ID}/`, paidThrough }] : []);
    if (url.pathname === "/api/workspace-launches" && method === "GET") return json(state.launched ? [launch(state.initialLaunchStatus || "succeeded")] : []);
    if (url.pathname === "/api/workspace-launches" && method === "POST") {
      state.launchPosts += 1;
      assert.equal(headers.get("idempotency-key"), BASIC_CANARY_LAUNCH_KEY);
      assert.deepEqual(JSON.parse(init.body), { name: "Basic Canary 2026-07-26", packageId: "basic", sizeGb: 10, autoRenew: false });
      state.launched = true;
      state.workspacePurchaseDebits += 1;
      state.tencentCvmPurchases += 1;
      state.tencentCbsPurchases += 1;
      state.initialLaunchStatus = "debit_pending";
      if (loseResponseAfter === "launch" && !state.lostResponses.has("launch")) {
        state.lostResponses.add("launch");
        throw new Error("simulated_launch_response_lost");
      }
      return json(launch("debit_pending"), 202);
    }
    if (url.pathname === `/api/workspace-launches/${BASIC_CANARY_LAUNCH_OPERATION_ID}`) {
      if (!state.launched) return json({ error: "workspace_launch_not_found" }, 404);
      state.launchPolls += 1;
      if (state.initialLaunchStatus === "succeeded") return json(launch("succeeded"));
      if (state.launchPolls === 1) return json(launch(state.initialLaunchStatus || "fulfilling_compute"));
      return json(launch(terminalStatus, terminalStatus));
    }
    if (url.pathname === "/api/gateway/keys") {
      const page = Number(url.searchParams.get("page") || "1");
      const pageSize = Number(url.searchParams.get("pageSize") || "20");
      const items = keyItems();
      if (!Number.isInteger(page) || page < 1 || !Number.isInteger(pageSize) || pageSize < 1) return json({ error: "invalid_page" }, 400);
      return gatewayPage(items.slice((page - 1) * pageSize, page * pageSize), page, pageSize, items.length);
    }
    if (url.pathname === `/api/gateway/keys/${BASIC_CANARY_KEY_ID}/usage`) return gatewayPage(state.modelRequests ? [usageRecord] : []);
    if (url.pathname === `/api/gateway/keys/${BASIC_CANARY_KEY_ID}/usage-summary`) {
      const count = state.modelRequests;
      return source({ totalRequests: count, totalInputTokens: count * 4, totalOutputTokens: count * 3, totalTokens: count * 7, totalActualCostUsdMicros: count * 120 });
    }
    if (url.pathname === "/api/billing/receipts") return source({ receipts: state.launched ? [receipt] : [], nextCursor: "", hasMore: false }, "ledger", state.launched ? "available" : "empty");
    if (url.pathname === `/api/billing/receipts/${receipt.receiptId}`) return source(receipt, "ledger");
    if (url.pathname === `/api/workspaces/${BASIC_CANARY_WORKSPACE_ID}/runtime-status`) return source({
      workspaceId: BASIC_CANARY_WORKSPACE_ID, runtimeId: "runtime-basic-canary", status: "running", ready: true,
      url: `https://workspace.medopl.cn/w/${BASIC_CANARY_WORKSPACE_ID}/`, serviceName: "workspace-service-basic-canary",
      checks: [{ name: "deployment_ready", ok: true }], access: { username: "opl", credentialStatus: "configured", credentialVersion: "v1" }
    }, "fabric");
    if (url.pathname === `/api/workspaces/${BASIC_CANARY_WORKSPACE_ID}/runtime-credentials/reveal`) return json({
      workspaceId: BASIC_CANARY_WORKSPACE_ID, access: { username: "opl", password: "workspace-password", credentialStatus: "configured", credentialVersion: "v1" }
    }, 200, { "cache-control": "private, no-store" });
    if (url.pathname === `/api/operator/workspaces/${BASIC_CANARY_WORKSPACE_ID}`) return source({
      workspace: nestedSource({ id: BASIC_CANARY_WORKSPACE_ID, state: "active", packageId: "basic" }, "control-plane"),
      workspaceKeyUsage: nestedSource({ keyId: BASIC_CANARY_KEY_ID, totalActualCostUsdMicros: state.modelRequests * 120 }, "sub2api"),
      receipt: nestedSource(receipt, "ledger"),
      resources: [
        resourceFact("compute", "ins-basic-canary", BASIC_CANARY_RESOLVED_INSTANCE_TYPE),
        resourceFact("storage", "disk-basic-canary", "CLOUD_BSSD"),
        resourceFact("attachment", "attachment-basic-canary", "/data", "attached"),
        resourceFact("runtime", "workspace-service-basic-canary", "workspace", "running")
      ]
    }, "control-plane+fabric+ledger");
    if (url.pathname === "/api/production/readiness") return json({ ready: true, cloudImagesReady: true, workspaceImagesReady: true, immutableImagesReady: true });
    return json({ error: "not_found" }, 404);
  };

  return {
    calls,
    state,
    fetchImpl,
    browserFactory: browserFactory(state),
    runtimePodEvidenceReader: async ({ workspaceId, expectedDigest }) => {
      state.podReads += 1;
      assert.equal(workspaceId, BASIC_CANARY_WORKSPACE_ID);
      assert.equal(expectedDigest, BASIC_CANARY_DIGEST);
      return {
        podName: "runtime-basic-canary-abc",
        nodeName: podNodeName,
        containerName: "workspace",
        ready: true,
        imageID: `containerd://${BASIC_CANARY_DIGEST}`,
        resources: { cpu: 2, memoryGb: 4 }
      };
    },
    cloudRevisionEvidenceReader: async ({ expectedMergedSha, expectedCloudDigest }) => {
      state.cloudRevisionReads += 1;
      assert.equal(expectedMergedSha, BASIC_CANARY_MERGED_SHA);
      assert.equal(expectedCloudDigest, BASIC_CANARY_CLOUD_DIGEST);
      if (cloudRevisionError) throw new Error(cloudRevisionError);
      return {
        mergedSha: BASIC_CANARY_MERGED_SHA,
        cloudDigest: BASIC_CANARY_CLOUD_DIGEST,
        services: {
          controlPlane: { revision: "101", imageID: `containerd://${BASIC_CANARY_CLOUD_IMAGE}` },
          fabric: { revision: "202", imageID: `containerd://${BASIC_CANARY_CLOUD_IMAGE}` },
          ledger: { revision: "303", imageID: `containerd://${BASIC_CANARY_CLOUD_IMAGE}` }
        }
      };
    }
  };
}

function basicCanaryOptions(fixture, { rechargeUsdMicros = "100000000" } = {}) {
  return {
    origin: "https://cloud.medopl.cn",
    fabricOrigin: "http://fabric.opl-cloud.svc:8082",
    internalServiceToken: "internal-service-token",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    customerPassword: BASIC_CANARY_CUSTOMER_PASSWORD,
    approvalJson: basicCanaryApprovalJson({ rechargeUsdMicros }),
    approvalId: BASIC_CANARY_APPROVAL_ID,
    confirmation: BASIC_CANARY_CONFIRMATION,
    mergedSha: BASIC_CANARY_MERGED_SHA,
    runId: "production-basic-canary-20260726",
    launchPollAttempts: 3,
    launchPollDelayMs: 0,
    usageAttempts: 2,
    usageRetryDelayMs: 0,
    browserTimeoutMs: 20,
    modelTimeoutMs: 20,
    fetchImpl: fixture.fetchImpl,
    fabricFetchImpl: fixture.fetchImpl,
    browserFactory: fixture.browserFactory,
    cloudRevisionEvidenceReader: fixture.cloudRevisionEvidenceReader,
    runtimePodEvidenceReader: fixture.runtimePodEvidenceReader,
    now: new Date("2026-07-26T00:00:00Z")
  };
}

function recoveredPrechargeBasicCanaryOptions(fixture, approval = {}) {
  return {
    ...basicCanaryOptions(fixture),
    approvalJson: recoveredPrechargeBasicCanaryApprovalJson(approval),
    fundingMode: "operator_precharge_recovery"
  };
}

const MANUAL_REVIEW_DIAGNOSE_TARGET = Object.freeze({
  accountId: "acct-manual-review-fixture",
  launchOperationId: "workspace-launch-manual-review-fixture",
  workspaceId: "ws-manual-review-fixture",
  computeAllocationId: "ca_manual-review-fixture",
  storageId: "vol_manual-review-fixture",
  nodePoolId: "np-manual-review-fixture",
  machineId: "np-manual-review-fixture-machine",
  nodeName: "10.20.30.31",
  cvmInstanceId: "ins-manual-review-fixture"
});

const COMPUTE_CLAIM_TARGET = Object.freeze({
  launchOperationId: "workspace-launch-compute-claim-fixture",
  accountId: "acct-compute-claim-fixture",
  workspaceId: "ws-compute-claim-fixture",
  computeAllocationId: "ca_compute_claim_fixture",
  storageId: "vol_compute_claim_fixture",
  packageId: "basic",
  poolId: "pool-basic-2c4g",
  nodePoolId: "np-workspace-basic",
  machineName: "np-workspace-basic-machine-fixture",
  nodeName: "10.20.30.41",
  cvmInstanceId: "ins-compute-claim-fixture",
  privateIp: "10.20.30.42",
  instanceType: "SA5.MEDIUM4",
  zone: "na-siliconvalley-1",
  chargeType: "PREPAID",
  periodMonths: 1,
  renewFlag: "NOTIFY_AND_MANUAL_RENEW",
  deadline: "2099-08-28T00:00:00Z"
});
const COMPUTE_CLAIM_CUSTOMER_EMAIL = "compute-claim-owner@example.test";
const COMPUTE_CLAIM_WORKSPACE_DIGEST = `sha256:${"e".repeat(64)}`;
const COMPUTE_CLAIM_ALLOWED_WRITES = Object.freeze([
  "claim_existing_cvm_node",
  "create_original_cbs",
  "create_original_pv_pvc_attachment",
  "upsert_original_gateway_secret",
  "create_original_workspace_runtime",
  "activate_original_workspace",
  "record_original_purchase_receipt"
]);
const COMPUTE_CLAIM_EXISTING_STORAGE_ALLOWED_WRITES = Object.freeze([
  "claim_existing_cvm_node",
  "reuse_original_cbs",
  "create_original_pv_pvc_attachment",
  "upsert_original_gateway_secret",
  "create_original_workspace_runtime",
  "activate_original_workspace",
  "record_original_purchase_receipt"
]);
const COMPUTE_CLAIM_FORBIDDEN_WRITES = Object.freeze([
  "create_launch", "debit", "recharge", "refund", "scale", "create_cvm", "create_second_cbs", "delete", "replace"
]);

function computeClaimStableSuffix(...parts) {
  return createHash("sha256").update(parts.join(":"), "utf8").digest("hex");
}

function computeClaimRecoveryResources(target = COMPUTE_CLAIM_TARGET, storageState = "storage_not_started", storageProviderResourceId = "") {
  const attachmentOperationId = `${target.launchOperationId}:attachment`;
  const runtimeOperationId = `${target.launchOperationId}:workspace:runtime`;
  return {
    computeOperationId: `${target.launchOperationId}:compute`,
    storageOperationId: `${target.launchOperationId}:storage`,
    storageState,
    storageProviderResourceId,
    attachmentId: `att_${computeClaimStableSuffix(attachmentOperationId).slice(0, 18)}`,
    attachmentOperationId,
    workspaceApiKeyId: "42",
    gatewaySecretRef: `opl-gateway-${computeClaimStableSuffix(target.workspaceId).slice(0, 16)}`,
    secretOperationId: `${target.launchOperationId}:workspace:secret:gateway-secret`,
    runtimeId: `rt_${computeClaimStableSuffix(target.workspaceId, runtimeOperationId).slice(0, 18)}`,
    runtimeOperationId,
    receiptOperationId: `${target.launchOperationId}:purchase-receipt`
  };
}

const COMPUTE_CLAIM_PRO_TARGET = Object.freeze({
  ...COMPUTE_CLAIM_TARGET,
  packageId: "pro",
  poolId: "pool-pro-8c16g",
  nodePoolId: "np-workspace-pro",
  machineName: "np-workspace-pro-machine-fixture",
  instanceType: "SA5.2XLARGE16"
});

function computeClaimProof(overrides = {}) {
  return {
    schemaVersion: 1,
    eligible: true,
    reason: "none",
    storageState: "storage_not_started",
    storageProviderResourceId: "",
    launchOperationId: COMPUTE_CLAIM_TARGET.launchOperationId,
    accountId: COMPUTE_CLAIM_TARGET.accountId,
    workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
    computeAllocationId: COMPUTE_CLAIM_TARGET.computeAllocationId,
    storageVolumeId: COMPUTE_CLAIM_TARGET.storageId,
    packageId: COMPUTE_CLAIM_TARGET.packageId,
    poolId: COMPUTE_CLAIM_TARGET.poolId,
    nodePoolId: COMPUTE_CLAIM_TARGET.nodePoolId,
    machineName: COMPUTE_CLAIM_TARGET.machineName,
    nodeName: COMPUTE_CLAIM_TARGET.nodeName,
    cvmInstanceId: COMPUTE_CLAIM_TARGET.cvmInstanceId,
    privateIp: COMPUTE_CLAIM_TARGET.privateIp,
    instanceType: COMPUTE_CLAIM_TARGET.instanceType,
    zone: COMPUTE_CLAIM_TARGET.zone,
    chargeType: COMPUTE_CLAIM_TARGET.chargeType,
    periodMonths: COMPUTE_CLAIM_TARGET.periodMonths,
    renewFlag: COMPUTE_CLAIM_TARGET.renewFlag,
    deadline: COMPUTE_CLAIM_TARGET.deadline,
    nodeOwnershipState: "unallocated",
    cvmOwnershipState: "recoverable",
    sub2apiMutationCount: 0,
    tencentMutationCount: 0,
    kubernetesMutationCount: 0,
    failureStage: "",
    providerErrorClass: "",
    evidence: {
      cvm: { attempted: 0, confirmed: 0, unknown: 0 },
      node: { attempted: 0, confirmed: 0, unknown: 0 }
    },
    ...overrides
  };
}

function computeClaimApprovalJson(overrides = {}) {
  return JSON.stringify({
    schemaVersion: 2,
    approvalId: "approval-compute-claim-fixture",
    expiresAt: "2099-08-28T00:00:00Z",
    mergedMainSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    workspaceImageDigest: COMPUTE_CLAIM_WORKSPACE_DIGEST,
    confirmation: "RECOVER_PROVEN_COMPUTE_AND_CONTINUE_ORIGINAL_LAUNCH",
    idempotencyKey: "compute-claim-http-fixture",
    recoveryKey: "compute-claim-recovery-fixture",
    customer: { email: COMPUTE_CLAIM_CUSTOMER_EMAIL, accountId: COMPUTE_CLAIM_TARGET.accountId },
    target: COMPUTE_CLAIM_TARGET,
    resources: computeClaimRecoveryResources(),
    attemptLimits: {
      claim: { sub2api: 0, tencent: 5, kubernetes: 1 },
      storage: 1,
      attachment: 1,
      secret: 1,
      runtime: 1,
      activation: 1,
      receipt: 1
    },
    allowedWrites: [...COMPUTE_CLAIM_ALLOWED_WRITES],
    forbiddenWrites: [...COMPUTE_CLAIM_FORBIDDEN_WRITES],
    ...overrides
  });
}

function computeClaimAccountAuthority(overrides = {}) {
  return source({
    items: [{
      accountId: COMPUTE_CLAIM_TARGET.accountId,
      email: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      role: "owner",
      status: "active",
      consoleUserId: "usr-compute-claim-fixture",
      sub2apiUserId: "42",
      ...overrides
    }],
    total: 1,
    page: 1,
    pageSize: 50
  }, "control-plane+sub2api");
}

function canonicalJsonForTest(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJsonForTest).join(",")}]`;
  if (!value || typeof value !== "object") return JSON.stringify(value);
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJsonForTest(value[key])}`).join(",")}}`;
}

function computeClaimApprovalDigestForTest(approvalJson) {
  return createHash("sha256").update(canonicalJsonForTest(JSON.parse(approvalJson))).digest("hex");
}

const WORKSPACE_LAUNCH_READBACK_ALLOWED_WRITES = Object.freeze([
  "confirm_original_secret_from_authoritative_readback",
  "create_original_workspace_runtime",
  "activate_original_workspace",
  "record_original_purchase_receipt"
]);
const WORKSPACE_LAUNCH_READBACK_FORBIDDEN_WRITES = Object.freeze([
  "create_launch", "debit", "recharge", "refund", "scale", "create_cvm", "create_second_cbs", "delete", "replace", "retry_unknown_stage_write"
]);

function workspaceLaunchReadbackProof(overrides = {}) {
	const target = {
		...COMPUTE_CLAIM_TARGET,
		storageGb: 10,
		autoRenew: false,
		priceVersion: "pilot-usd-2026-07-v1",
		totalChargeUsdMicros: 52580000,
		periodStart: "2099-07-28T00:00:00Z",
		paidThrough: "2099-08-27T00:00:00Z",
		billingAnchorDay: 28
	};
	const operationIdentity = (idempotencyKey, suffix, providerOperationId, present = true, readbackBindingDigest = "") => ({
		idempotencyKey,
		fabricRecordId: present ? `fop-${suffix}-fixture` : "",
		fabricOperationId: present ? `op-${suffix}-fixture` : "",
		requestHash: present ? computeClaimStableSuffix("request", suffix) : "",
		resourceOperationId: present ? idempotencyKey : "",
		providerOperationId: present ? providerOperationId : "",
		readbackBindingDigest
	});
	const ownershipId = "owner-compute-claim-fixture";
	return {
    schemaVersion: 1,
    eligible: true,
    reason: "none",
    stage: "secret",
    customer: { email: COMPUTE_CLAIM_CUSTOMER_EMAIL, accountId: COMPUTE_CLAIM_TARGET.accountId, ownerUserId: "usr-compute-claim-fixture" },
		target,
		resources: {
      computeAllocationId: COMPUTE_CLAIM_TARGET.computeAllocationId,
      computeProviderResourceId: COMPUTE_CLAIM_TARGET.cvmInstanceId,
			storageVolumeId: COMPUTE_CLAIM_TARGET.storageId,
			storageProviderResourceId: "disk-existing-fixture",
			storageZone: COMPUTE_CLAIM_TARGET.zone,
			storageSizeGb: 10,
			storageChargeType: "PREPAID",
			storageRenewFlag: "NOTIFY_AND_MANUAL_RENEW",
			storageDeadline: "2099-08-29T00:00:00Z",
			attachmentId: "attachment-compute-claim-fixture",
			attachmentProviderId: "pv/volume-fixture:pvc/volume-fixture-data",
			gatewaySecretRef: `opl-gateway-${computeClaimStableSuffix(COMPUTE_CLAIM_TARGET.workspaceId).slice(0, 16)}`,
			gatewaySecretFingerprint: `sha256:${"c".repeat(64)}`,
			workspaceApiKeyId: 42,
			runtimeId: "",
			runtimeServiceName: "",
			receiptId: ""
		},
		operationIds: {
			launchOperationId: COMPUTE_CLAIM_TARGET.launchOperationId,
			launchRequestHash: computeClaimStableSuffix("launch", COMPUTE_CLAIM_TARGET.launchOperationId),
			machineOwnershipId: ownershipId,
			compute: operationIdentity(`${COMPUTE_CLAIM_TARGET.launchOperationId}:compute`, "compute", ownershipId),
			storage: operationIdentity(`${COMPUTE_CLAIM_TARGET.launchOperationId}:storage`, "storage", "op-storage-provider-fixture"),
			attachment: operationIdentity(`${COMPUTE_CLAIM_TARGET.launchOperationId}:attachment`, "attachment", `${COMPUTE_CLAIM_TARGET.launchOperationId}:attachment`),
			secret: operationIdentity(`${COMPUTE_CLAIM_TARGET.launchOperationId}:workspace:secret:gateway-secret`, "secret", "", true, "d".repeat(64)),
			runtime: operationIdentity(`${COMPUTE_CLAIM_TARGET.launchOperationId}:workspace:runtime`, "runtime", "", false),
			activationOperationId: `${COMPUTE_CLAIM_TARGET.launchOperationId}:activation`,
			receiptOperationId: `${COMPUTE_CLAIM_TARGET.launchOperationId}:purchase-receipt`
		},
    workspaceImageDigest: COMPUTE_CLAIM_WORKSPACE_DIGEST,
    attemptBudget: { attempted: 1, confirmed: 0, unknown: 1, max: 1 },
    allowedWrites: [...WORKSPACE_LAUNCH_READBACK_ALLOWED_WRITES],
    forbiddenWrites: [...WORKSPACE_LAUNCH_READBACK_FORBIDDEN_WRITES],
    sub2apiMutationCount: 0,
    tencentMutationCount: 0,
    kubernetesMutationCount: 0,
    ...overrides
  };
}

function workspaceLaunchReadbackApprovalJson(proof = workspaceLaunchReadbackProof(), overrides = {}) {
  return JSON.stringify({
    schemaVersion: 1,
    approvalId: "approval-readback-fixture",
    expiresAt: "2099-08-28T00:00:00Z",
    mergedMainSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    workspaceImageDigest: proof.workspaceImageDigest,
    confirmation: "RECOVER_UNKNOWN_WORKSPACE_LAUNCH_STAGE_FROM_AUTHORITATIVE_READBACK",
    idempotencyKey: "workspace-readback-http-fixture",
    recoveryKey: "workspace-readback-recovery-fixture",
    stage: proof.stage,
    customer: proof.customer,
    target: proof.target,
    resources: proof.resources,
    operationIds: proof.operationIds,
    attemptBudget: proof.attemptBudget,
    allowedWrites: proof.allowedWrites,
    forbiddenWrites: proof.forbiddenWrites,
    ...overrides
  });
}

function workspaceLaunchReadbackRecoveryResponse(proof, status = "preparing") {
  return {
    operationId: COMPUTE_CLAIM_TARGET.launchOperationId,
    accountId: COMPUTE_CLAIM_TARGET.accountId,
    workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
    status,
    phase: status === "succeeded" ? "succeeded" : "runtime_starting",
    continuationAttemptBudgets: { [proof.stage]: { attempted: 1, confirmed: 1, unknown: 0, max: 1 } },
    readbackRecoveryProof: proof
  };
}

function assertWorkspaceLaunchArtifactSafe(artifact, sensitiveValues = []) {
  const forbiddenKeys = new Set([
    "email", "privateIp", "machineName", "nodeName", "cvmInstanceId", "target", "proof", "approval",
    "gatewaySecretRef", "gatewaySecretFingerprint", "credential", "capability", "providerRequestId",
    "operationIds", "fabricRecordId", "fabricOperationId", "providerOperationId", "requestHash", "resourceOperationId", "readbackBindingDigest"
  ]);
  const visit = (value) => {
    if (Array.isArray(value)) {
      value.forEach(visit);
      return;
    }
    if (!value || typeof value !== "object") return;
    for (const [key, item] of Object.entries(value)) {
      assert.equal(forbiddenKeys.has(key), false, `unsafe artifact key ${key}`);
      visit(item);
    }
  };
  visit(artifact);
  const serialized = JSON.stringify(artifact);
  for (const value of sensitiveValues.filter(Boolean)) assert.equal(serialized.includes(String(value)), false, `unsafe artifact value ${value}`);
}

test("Workspace launch readback artifacts and continuation handoff use explicit safe allowlists", () => {
  assert.equal(typeof productionLiveQa.workspaceLaunchReadbackArtifact, "function");
  assert.equal(typeof productionLiveQa.workspaceLaunchContinuationHandoff, "function");
  const proof = workspaceLaunchReadbackProof();
  const release = { mergedSha: BASIC_CANARY_MERGED_SHA, cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST };
  const rawDiagnosis = {
    schemaVersion: 1,
    operationMode: "workspace_launch_readback_diagnose",
    status: "proven",
    recoveryEligible: true,
    errorCode: "none",
    release,
    target: COMPUTE_CLAIM_TARGET,
    proof,
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    verifiedAt: "2026-08-28T00:00:00.000Z"
  };
  const approvalDigest = "a".repeat(64);
  const rawRecovery = {
    schemaVersion: 1,
    operationMode: "workspace_launch_readback_recover",
    status: "converged",
    recoveryEligible: true,
    errorCode: "none",
    release,
    target: COMPUTE_CLAIM_TARGET,
    stage: proof.stage,
    proof,
    approval: { approvalId: "approval-readback-fixture", approvalDigest },
    operation: {
      operationId: COMPUTE_CLAIM_TARGET.launchOperationId,
      accountId: COMPUTE_CLAIM_TARGET.accountId,
      workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
      status: "waiting",
      phase: "runtime_starting",
      attemptBudget: { attempted: 1, confirmed: 1, unknown: 0, max: 1 }
    },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    backgroundMutationCountsState: "unknown",
    verifiedAt: "2026-08-28T00:00:00.000Z"
  };
  const diagnosis = productionLiveQa.workspaceLaunchReadbackArtifact(rawDiagnosis);
  const recovery = productionLiveQa.workspaceLaunchReadbackArtifact(rawRecovery);
  const blocked = productionLiveQa.workspaceLaunchReadbackArtifact({
    schemaVersion: 2,
    operationMode: "workspace_launch_readback_recover",
    status: "blocked",
    recoveryEligible: false,
    errorCode: "workspace_launch_readback_recovery_failed",
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    backgroundMutationCountsState: "unknown"
  });
  assert.deepEqual(Object.keys(diagnosis), [
    "schemaVersion", "operationMode", "status", "recoveryEligible", "errorCode", "release", "stage", "bindingDigest",
    "attemptBudget", "runnerDirectMutationCounts", "verifiedAt"
  ]);
  assert.deepEqual(Object.keys(recovery), [
    "schemaVersion", "operationMode", "status", "recoveryEligible", "errorCode", "release", "stage", "approvalBinding",
    "operation", "runnerDirectMutationCounts", "backgroundMutationCountsState", "verifiedAt"
  ]);
  assert.deepEqual(Object.keys(blocked), [
    "schemaVersion", "operationMode", "status", "recoveryEligible", "errorCode", "runnerDirectMutationCounts", "backgroundMutationCountsState"
  ]);
  assert.match(diagnosis.bindingDigest, /^[a-f0-9]{64}$/);
  assert.equal(recovery.approvalBinding.approvalDigest, approvalDigest);
  assert.equal(recovery.approvalBinding.bindingDigest, diagnosis.bindingDigest);

  const missingStageBinding = structuredClone(rawDiagnosis);
  missingStageBinding.proof.operationIds.secret.readbackBindingDigest = "";
  assert.throws(() => productionLiveQa.workspaceLaunchReadbackArtifact(missingStageBinding), /workspace_launch_readback_proof_invalid/);
  const wrongStageBinding = structuredClone(rawDiagnosis);
  wrongStageBinding.proof.operationIds.attachment.readbackBindingDigest = "e".repeat(64);
  assert.throws(() => productionLiveQa.workspaceLaunchReadbackArtifact(wrongStageBinding), /workspace_launch_readback_proof_invalid/);

  const launch = computeClaimContinuationLaunch({ phase: "succeeded", status: "succeeded" });
  const runtime = {
    workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
    runtimeId: computeClaimRecoveryResources().runtimeId,
    serviceName: "workspace-service-compute-claim-fixture",
    status: "running",
    ready: true,
    url: `https://workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/`
  };
  const continuation = productionLiveQa.workspaceLaunchContinuationHandoff({
    schemaVersion: 2,
    operationMode: "compute_claim_recover_continuation",
    status: "succeeded",
    recoveryEligible: true,
    errorCode: "none",
    release,
    target: COMPUTE_CLAIM_TARGET,
    launch,
    runtime,
    receipt: {
      receiptId: launch.receiptId,
      workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
      status: "completed"
    },
    recovery: {
      approvalId: "approval-readback-fixture",
      approvalDigest,
      recoveryKey: "workspace-readback-recovery-fixture",
      workspaceImageDigest: proof.workspaceImageDigest
    },
    terminalEvidence: {
      workspacePodImageID: `containerd://${proof.workspaceImageDigest}`,
      workspaceUrlHttpStatus: 200
    },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    backgroundMutationCountsState: "unknown",
    verifiedAt: "2026-08-28T00:00:00.000Z"
  }, recovery);
  assert.deepEqual(Object.keys(continuation), [
    "schemaVersion", "operationMode", "status", "recoveryEligible", "errorCode", "release", "handoff",
    "runnerDirectMutationCounts", "backgroundMutationCountsState", "verifiedAt"
  ]);
  assert.equal(continuation.handoff.recoveryApprovalDigest, approvalDigest);
  assert.equal(continuation.handoff.recoveryBindingDigest, diagnosis.bindingDigest);
  assert.deepEqual(continuation.handoff.terminalEvidence, {
    workspacePodImageID: `containerd://${proof.workspaceImageDigest}`,
    workspaceUrlHttpStatus: 200
  });
  for (const artifact of [diagnosis, recovery, blocked, continuation]) {
    assertWorkspaceLaunchArtifactSafe(artifact, [
      proof.customer.email,
      COMPUTE_CLAIM_TARGET.privateIp,
      COMPUTE_CLAIM_TARGET.machineName,
      COMPUTE_CLAIM_TARGET.nodeName,
      COMPUTE_CLAIM_TARGET.cvmInstanceId,
      proof.resources.gatewaySecretRef,
      proof.resources.gatewaySecretFingerprint,
      proof.operationIds.storage.fabricOperationId,
      proof.operationIds.storage.providerOperationId
    ]);
  }
});

test("compute claim continuation handoff binds the recovery artifact digest", () => {
  const release = { mergedSha: BASIC_CANARY_MERGED_SHA, cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST };
  const approvalDigest = computeClaimApprovalDigestForTest(computeClaimApprovalJson());
  const recoveryArtifact = {
    schemaVersion: 2,
    operationMode: "compute_claim_recover",
    status: "claimed",
    recoveryEligible: true,
    errorCode: "none",
    release,
    target: { ...COMPUTE_CLAIM_TARGET },
    proof: computeClaimProof({
      nodeOwnershipState: "target_owned",
      cvmOwnershipState: "target_owned",
      evidence: {
        cvm: { attempted: 0, confirmed: 0, unknown: 0, missing: [] },
        node: { attempted: 0, confirmed: 0, unknown: 0, missing: [] }
      }
    }),
    approval: { approvalId: "approval-compute-claim-fixture", approvalDigest },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
  };
  const launch = computeClaimContinuationLaunch({ phase: "succeeded", status: "succeeded" });
  const runtime = {
    workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
    runtimeId: computeClaimRecoveryResources().runtimeId,
    serviceName: "workspace-service-compute-claim-fixture",
    status: "running",
    ready: true,
    url: `https://workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/`
  };
  const raw = {
    schemaVersion: 2,
    operationMode: "compute_claim_recover_continuation",
    status: "succeeded",
    recoveryEligible: true,
    errorCode: "none",
    release,
    target: { ...COMPUTE_CLAIM_TARGET },
    launch,
    runtime,
    receipt: { receiptId: launch.receiptId, workspaceId: COMPUTE_CLAIM_TARGET.workspaceId, status: "completed" },
    recovery: {
      approvalId: "approval-compute-claim-fixture",
      approvalDigest,
      recoveryKey: "compute-claim-recovery-fixture",
      workspaceImageDigest: COMPUTE_CLAIM_WORKSPACE_DIGEST
    },
    terminalEvidence: {
      workspacePodImageID: `containerd://${COMPUTE_CLAIM_WORKSPACE_DIGEST}`,
      workspaceUrlHttpStatus: 200
    },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    backgroundMutationCountsState: "unknown",
    verifiedAt: "2026-08-28T00:00:00.000Z"
  };

  const handoff = productionLiveQa.workspaceLaunchContinuationHandoff(raw, recoveryArtifact);
  assert.equal(handoff.handoff.recoveryBindingDigest,
    createHash("sha256").update(canonicalJsonForTest(recoveryArtifact)).digest("hex"));
  assert.equal(handoff.handoff.recoveryApprovalDigest, approvalDigest);
  assert.deepEqual(handoff.handoff.terminalEvidence, raw.terminalEvidence);
  assert.equal(Object.hasOwn(handoff, "target"), false);
  assert.equal(Object.hasOwn(handoff, "launch"), false);
  assert.equal(Object.hasOwn(handoff, "runtime"), false);
  assert.equal(Object.hasOwn(handoff, "receipt"), false);
  assert.equal(Object.hasOwn(handoff, "recovery"), false);

  const drifted = structuredClone(recoveryArtifact);
  drifted.unexpected = true;
  assert.throws(() => productionLiveQa.workspaceLaunchContinuationHandoff(raw, drifted), /workspace_launch_continuation_artifact_invalid/);
});

test("workspace launch readback diagnosis is GET-only and binds the exact unknown stage", async () => {
  assert.equal(typeof productionLiveQa.diagnoseWorkspaceLaunchReadbackRecovery, "function");
  const calls = [];
  const proof = workspaceLaunchReadbackProof();
  const result = await productionLiveQa.diagnoseWorkspaceLaunchReadbackRecovery({
    target: COMPUTE_CLAIM_TARGET,
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl: async (input, init = {}) => {
      const url = new URL(String(input));
      calls.push({ method: init.method || "GET", path: url.pathname });
      if (url.pathname === "/api/auth/login") return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
        "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly", "x-opl-csrf-token": "csrf-readback"
      });
      return json(proof);
    }
  });

  assert.equal(result.operationMode, "workspace_launch_readback_diagnose");
  assert.equal(result.status, "proven");
  assert.deepEqual(result.proof, proof);
  assert.deepEqual(result.target, COMPUTE_CLAIM_TARGET);
  assert.deepEqual(result.runnerDirectMutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
  assert.deepEqual(calls, [
    { method: "POST", path: "/api/auth/login" },
    { method: "GET", path: `/api/operator/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}/readback-recovery-proof` }
  ]);
});

test("workspace launch readback rejects either provider deadline before paidThrough", async () => {
  for (const [name, target, mutateProof] of [
    ["compute", { ...COMPUTE_CLAIM_TARGET, deadline: "2099-08-26T00:00:00Z" }, (proof) => {
      proof.target.deadline = "2099-08-26T00:00:00Z";
    }],
    ["storage", COMPUTE_CLAIM_TARGET, (proof) => {
      proof.resources.storageDeadline = "2099-08-26T00:00:00Z";
    }]
  ]) {
    const proof = workspaceLaunchReadbackProof();
    mutateProof(proof);
    let recoveryCalls = 0;
    await assert.rejects(() => productionLiveQa.recoverWorkspaceLaunchReadbackRecovery({
      target,
      approvalJson: workspaceLaunchReadbackApprovalJson(proof),
      approvalId: "approval-readback-fixture",
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      origin: "https://cloud.medopl.cn",
      adminEmail: ADMIN_EMAIL,
      adminPassword: ADMIN_PASSWORD,
      customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      internalServiceToken: "workspace-readback-capability",
      kubeconfigPath: "/run/secrets/kubeconfig",
      namespace: "opl-cloud",
      cloudRevisionEvidenceReader: async () => { recoveryCalls += 1; return computeClaimCloudRevisionEvidence(); },
      fetchImpl: async () => { recoveryCalls += 1; return json(proof); },
      now: new Date("2026-08-28T00:00:00Z")
    }), /workspace_launch_readback_(?:approval_invalid|proof_invalid)/, name);
    assert.equal(recoveryCalls, 0, `${name} deadline crossed recovery preflight`);

    let diagnosisCalls = 0;
    await assert.rejects(() => productionLiveQa.diagnoseWorkspaceLaunchReadbackRecovery({
      target,
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      origin: "https://cloud.medopl.cn",
      adminEmail: ADMIN_EMAIL,
      adminPassword: ADMIN_PASSWORD,
      customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      kubeconfigPath: "/run/secrets/kubeconfig",
      namespace: "opl-cloud",
      cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
      fetchImpl: async (input) => {
        diagnosisCalls += 1;
        if (new URL(String(input)).pathname === "/api/auth/login") {
          return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
            "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly", "x-opl-csrf-token": "csrf-readback"
          });
        }
        return json(proof);
      }
    }), /workspace_launch_readback_proof_invalid/, name);
    assert.equal(diagnosisCalls, 2, `${name} diagnosis did not stop at proof validation`);
  }
});

test("workspace launch readback rejects a three-field target before network access", async () => {
  let calls = 0;
  await assert.rejects(() => productionLiveQa.diagnoseWorkspaceLaunchReadbackRecovery({
    target: {
      launchOperationId: COMPUTE_CLAIM_TARGET.launchOperationId,
      accountId: COMPUTE_CLAIM_TARGET.accountId,
      workspaceId: COMPUTE_CLAIM_TARGET.workspaceId
    },
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => { calls += 1; return computeClaimCloudRevisionEvidence(); },
    fetchImpl: async () => { calls += 1; return json({}); }
  }), /workspace_launch_readback_target_invalid/);
  assert.equal(calls, 0);
});

test("workspace launch readback recovery rejects approval release customer and target drift before network access", async () => {
  const proof = workspaceLaunchReadbackProof();
  const cases = [
    ["main SHA", { mergedMainSha: "f".repeat(40) }],
    ["Cloud digest", { cloudImageDigest: `sha256:${"f".repeat(64)}` }],
    ["customer", { customer: { ...proof.customer, email: "other-owner@example.test" } }],
    ["target", { target: { ...proof.target, cvmInstanceId: "ins-other-fixture" } }]
  ];
  for (const [name, approvalOverrides] of cases) {
    let externalCalls = 0;
    await assert.rejects(() => productionLiveQa.recoverWorkspaceLaunchReadbackRecovery({
      target: COMPUTE_CLAIM_TARGET,
      approvalJson: workspaceLaunchReadbackApprovalJson(proof, approvalOverrides),
      approvalId: "approval-readback-fixture",
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      origin: "https://cloud.medopl.cn",
      adminEmail: ADMIN_EMAIL,
      adminPassword: ADMIN_PASSWORD,
      customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      internalServiceToken: "workspace-readback-capability",
      kubeconfigPath: "/run/secrets/kubeconfig",
      namespace: "opl-cloud",
      cloudRevisionEvidenceReader: async () => {
        externalCalls += 1;
        throw new Error("unexpected_cloud_revision_access");
      },
      fetchImpl: async () => {
        externalCalls += 1;
        throw new Error("unexpected_network_access");
      },
      now: new Date("2026-08-28T00:00:00Z")
    }), /workspace_launch_readback_(?:approval_invalid|proof_drift|customer_identity_mismatch)/, name);
    assert.equal(externalCalls, 0, `${name} drift crossed an external boundary`);
  }
});

test("workspace launch readback recovery validates persisted POST proof without a diagnosis GET", async () => {
  assert.equal(typeof productionLiveQa.recoverWorkspaceLaunchReadbackRecovery, "function");
  const proof = workspaceLaunchReadbackProof();
  const approvalJson = workspaceLaunchReadbackApprovalJson(proof);
  const calls = [];
  const result = await productionLiveQa.recoverWorkspaceLaunchReadbackRecovery({
    target: COMPUTE_CLAIM_TARGET,
    approvalJson,
    approvalId: "approval-readback-fixture",
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
    internalServiceToken: "workspace-readback-capability",
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl: async (input, init = {}) => {
      const url = new URL(String(input));
      const body = init.body ? JSON.parse(String(init.body)) : null;
      calls.push({ method: init.method || "GET", path: url.pathname, headers: new Headers(init.headers), body });
      if (url.pathname === "/api/auth/login") return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
        "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly", "x-opl-csrf-token": "csrf-readback"
      });
      if (url.pathname.endsWith("/readback-recovery-proof")) throw new Error("unexpected_readback_proof_get");
      return json(workspaceLaunchReadbackRecoveryResponse(proof));
    },
    now: new Date("2026-08-28T00:00:00Z")
  });

  const mutations = calls.filter(({ method }) => method !== "GET" && method !== "POST" || false);
  const recoveryPosts = calls.filter(({ method, path }) => method === "POST" && path.endsWith("/recover"));
  assert.equal(mutations.length, 0);
  assert.equal(recoveryPosts.length, 1);
  assert.deepEqual(recoveryPosts[0].body.approval, {
    ...JSON.parse(approvalJson),
    approvalDigest: createHash("sha256").update(canonicalJsonForTest(JSON.parse(approvalJson))).digest("hex")
  });
  assert.equal(recoveryPosts[0].headers.get("idempotency-key"), "workspace-readback-http-fixture");
  assert.equal(recoveryPosts[0].headers.get("x-opl-compute-claim-capability"), "workspace-readback-capability");
  assert.equal(result.operationMode, "workspace_launch_readback_recover");
  assert.equal(result.status, "converged");
	assert.equal(calls.filter(({ method }) => method === "GET").length, 0);

  const driftedProof = workspaceLaunchReadbackProof({ resources: { ...proof.resources, storageProviderResourceId: "disk-drifted-fixture" } });
	let driftPosts = 0;
  await assert.rejects(() => productionLiveQa.recoverWorkspaceLaunchReadbackRecovery({
    target: COMPUTE_CLAIM_TARGET,
    approvalJson,
    approvalId: "approval-readback-fixture",
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
    internalServiceToken: "workspace-readback-capability",
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl: async (input, init = {}) => {
      const url = new URL(String(input));
      if (url.pathname === "/api/auth/login") return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
        "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly", "x-opl-csrf-token": "csrf-readback"
      });
		  if ((init.method || "GET") === "POST") {
			driftPosts += 1;
			return json(workspaceLaunchReadbackRecoveryResponse(driftedProof));
		  }
		  throw new Error("unexpected_readback_proof_get");
		},
    now: new Date("2026-08-28T00:00:00Z")
	}), /workspace_launch_readback_proof_drift/);
	assert.equal(driftPosts, 1);
});

test("workspace launch readback recovery retries one lost POST response with the exact persisted request", async () => {
  for (const status of ["preparing", "waiting", "succeeded"]) {
    const proof = workspaceLaunchReadbackProof();
    const approvalJson = workspaceLaunchReadbackApprovalJson(proof);
    const recoveryPosts = [];
    let firstPost = true;
    const result = await productionLiveQa.recoverWorkspaceLaunchReadbackRecovery({
      target: COMPUTE_CLAIM_TARGET,
      approvalJson,
      approvalId: "approval-readback-fixture",
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      origin: "https://cloud.medopl.cn",
      adminEmail: ADMIN_EMAIL,
      adminPassword: ADMIN_PASSWORD,
      customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      internalServiceToken: "workspace-readback-capability",
      kubeconfigPath: "/run/secrets/kubeconfig",
      namespace: "opl-cloud",
      cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
      fetchImpl: async (input, init = {}) => {
        const url = new URL(String(input));
        if (url.pathname === "/api/auth/login") return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
          "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly", "x-opl-csrf-token": "csrf-readback"
        });
        if (url.pathname.endsWith("/readback-recovery-proof")) throw new Error("unexpected_readback_proof_get");
        recoveryPosts.push({ headers: new Headers(init.headers), body: String(init.body) });
        if (firstPost) {
          firstPost = false;
          throw new Error("simulated_http_response_lost_after_persist");
        }
        return json(workspaceLaunchReadbackRecoveryResponse(proof, status));
      },
      now: new Date("2026-08-28T00:00:00Z")
    });
    assert.equal(result.operation.status, status, status);
    assert.equal(recoveryPosts.length, 2, status);
    assert.equal(recoveryPosts[0].body, recoveryPosts[1].body, status);
    assert.equal(recoveryPosts[0].headers.get("idempotency-key"), recoveryPosts[1].headers.get("idempotency-key"), status);
  }
});

test("workspace launch readback recovery lets the server decide an expired exact persisted replay", async () => {
  const proof = workspaceLaunchReadbackProof();
  const approvalJson = workspaceLaunchReadbackApprovalJson(proof, { expiresAt: "2026-08-27T00:00:00Z" });
  const calls = [];
  const result = await productionLiveQa.recoverWorkspaceLaunchReadbackRecovery({
    target: COMPUTE_CLAIM_TARGET,
    approvalJson,
    approvalId: "approval-readback-fixture",
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
    internalServiceToken: "workspace-readback-capability",
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl: async (input, init = {}) => {
      const url = new URL(String(input));
      calls.push({ method: init.method || "GET", path: url.pathname });
      if (url.pathname === "/api/auth/login") return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
        "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly", "x-opl-csrf-token": "csrf-readback"
      });
      return json(workspaceLaunchReadbackRecoveryResponse(proof, "waiting"));
    },
    now: new Date("2026-08-28T00:00:00Z")
  });
  assert.equal(result.operation.status, "waiting");
  assert.deepEqual(calls.map(({ method }) => method), ["POST", "POST"]);
});

test("workspace launch readback CLI modes emit redacted blocked artifacts on exceptions", async () => {
  const target = JSON.stringify({
    launchOperationId: COMPUTE_CLAIM_TARGET.launchOperationId,
    accountId: COMPUTE_CLAIM_TARGET.accountId,
    workspaceId: COMPUTE_CLAIM_TARGET.workspaceId
  });
  for (const [flag, mode, errorCode] of [
    ["--workspace-launch-readback-diagnose", "workspace_launch_readback_diagnose", "workspace_launch_readback_diagnosis_failed"],
    ["--workspace-launch-readback-recover", "workspace_launch_readback_recover", "workspace_launch_readback_recovery_failed"]
  ]) {
    let stdout = "";
    let stderr = "";
    const code = await runProductionLiveQaCli({
      argv: [flag, "--workspace-launch-target-json", target, ...(flag.endsWith("recover") ? ["--approval-id", "approval-readback-fixture"] : [])],
      env: {},
      stdout: { write: (chunk) => { stdout += chunk; } },
      stderr: { write: (chunk) => { stderr += chunk; } }
    });
		assert.equal(code, 1);
		assert.deepEqual(JSON.parse(stdout), {
		  schemaVersion: 2,
      operationMode: mode,
      status: "blocked",
      recoveryEligible: false,
      errorCode,
      runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
      ...(flag.endsWith("recover") ? { backgroundMutationCountsState: "unknown" } : {})
    });
    assert.doesNotMatch(`${stdout}\n${stderr}`, /password|secret|token|@/i);
  }
});

test("workspace launch readback CLI modes reject read-only and manual-review modes before network access", async () => {
  const target = JSON.stringify(COMPUTE_CLAIM_TARGET);
  for (const [readbackFlag, conflictingFlag] of [
    ["--workspace-launch-readback-diagnose", "--read-only"],
    ["--workspace-launch-readback-recover", "--read-only"],
    ["--workspace-launch-readback-diagnose", "--manual-review-diagnose"],
    ["--workspace-launch-readback-recover", "--manual-review-diagnose"]
  ]) {
    let externalCalls = 0;
    let stdout = "";
    const code = await runProductionLiveQaCli({
      argv: [
        readbackFlag,
        conflictingFlag,
        "--workspace-launch-target-json",
        target,
        "--diagnose-target-json",
        JSON.stringify(MANUAL_REVIEW_DIAGNOSE_TARGET),
        "--fabric-pod",
        "fabric-fixture",
        "--fabric-namespace",
        "opl-cloud"
      ],
      env: {
        OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
        OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
        OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD,
        KUBECONFIG: "/run/secrets/kubeconfig"
      },
      stdout: { write: (chunk) => { stdout += chunk; } },
      stderr: { write: () => {} },
      fetchImpl: async () => {
        externalCalls += 1;
        throw new Error("unexpected_network_access");
      },
      execFileImpl: async () => {
        externalCalls += 1;
        throw new Error("unexpected_kubectl_access");
      },
      browserFactory: async () => {
        externalCalls += 1;
        throw new Error("unexpected_browser_access");
      }
    });
    assert.equal(code, 1, `${readbackFlag} accepted ${conflictingFlag}`);
    assert.equal(externalCalls, 0, `${readbackFlag} accessed an external boundary before rejecting ${conflictingFlag}`);
    assert.equal(JSON.parse(stdout).status, "blocked");
  }
});

test("workspace launch readback diagnosis CLI emits a safe allowlist and keeps raw proof only in an explicit temp file", async () => {
	let stdout = "";
	const proof = workspaceLaunchReadbackProof();
	const common = {
		argv: ["--workspace-launch-readback-diagnose", "--workspace-launch-target-json", JSON.stringify(COMPUTE_CLAIM_TARGET)],
		env: {
      OPL_MERGED_SHA: BASIC_CANARY_MERGED_SHA,
      OPL_WORKSPACE_LAUNCH_READBACK_CLOUD_DIGEST: BASIC_CANARY_CLOUD_DIGEST,
      OPL_WORKSPACE_LAUNCH_READBACK_CUSTOMER_EMAIL: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD,
      OPL_K8S_NAMESPACE: "opl-cloud",
      KUBECONFIG: "/run/secrets/kubeconfig"
		},
		stderr: { write: () => {} },
		cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl: async (input) => {
      const url = new URL(String(input));
      if (url.pathname === "/api/auth/login") {
        return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
          "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly",
          "x-opl-csrf-token": "csrf-readback"
        });
		  }
		  return json(proof);
		}
	};
	const code = await runProductionLiveQaCli({ ...common, stdout: { write: (chunk) => { stdout += chunk; } } });

	assert.equal(code, 0);
	const artifact = JSON.parse(stdout);
	assert.equal(artifact.schemaVersion, 2);
	assert.equal(artifact.operationMode, "workspace_launch_readback_diagnose");
	assert.equal(artifact.status, "proven");
	assert.equal(artifact.stage, proof.stage);
	assert.equal(artifact.target, undefined);
	assert.equal(artifact.proof, undefined);
	assertWorkspaceLaunchArtifactSafe(artifact, [proof.customer.email, proof.target.privateIp, proof.target.cvmInstanceId, proof.resources.gatewaySecretRef]);

	const root = await mkdtemp(join(tmpdir(), "opl-readback-raw-"));
	try {
		const rawPath = join(root, "readback.raw.json");
		let protectedStdout = "";
		const protectedCode = await runProductionLiveQaCli({
		  ...common,
		  env: { ...common.env, OPL_WORKSPACE_LAUNCH_READBACK_RAW_RESULT_PATH: rawPath },
		  stdout: { write: (chunk) => { protectedStdout += chunk; } }
		});
		assert.equal(protectedCode, 0);
		assert.equal(protectedStdout, "");
		const raw = JSON.parse(await readFile(rawPath, "utf8"));
		assert.deepEqual(raw.target, COMPUTE_CLAIM_TARGET);
		assert.deepEqual(raw.proof, proof);
	} finally {
		await rm(root, { recursive: true, force: true });
	}
});

test("recovered Workspace E2E requires an independent single-model approval and succeeded continuation before network access", async () => {
  assert.equal(typeof productionLiveQa.verifyRecoveredWorkspaceE2E, "function");
  let networkCalls = 0;
  const base = {
    origin: "https://cloud.medopl.cn",
    mergedSha: BASIC_CANARY_MERGED_SHA,
    customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
    customerPassword: "customer-password-fixture",
    approvalId: "approval-recovered-e2e-fixture",
    confirmation: "CONFIRM_SINGLE_MODEL_REQUEST_FOR_RECOVERED_WORKSPACE",
	  continuationEvidence: recoveredWorkspaceE2EContinuationFixture(),
    fetchImpl: async () => {
      networkCalls += 1;
      return json({ error: "unexpected_network" }, 500);
    },
    now: new Date("2026-08-28T00:00:00Z")
  };

  await assert.rejects(() => productionLiveQa.verifyRecoveredWorkspaceE2E(base), /recovered_workspace_e2e_approval_invalid/);
  assert.equal(networkCalls, 0);

  const approval = {
    schemaVersion: 1,
    approvalId: base.approvalId,
    expiresAt: "2099-08-28T00:00:00Z",
    confirmation: base.confirmation,
    mergedMainSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    workspaceImageDigest: COMPUTE_CLAIM_WORKSPACE_DIGEST,
	  recoveryApprovalId: "approval-compute-claim-fixture",
	  recoveryApprovalDigest: computeClaimApprovalDigestForTest(computeClaimApprovalJson()),
	  recoveryBindingDigest: "b".repeat(64),
    recoveryKey: "compute-claim-recovery-fixture",
    customer: { email: COMPUTE_CLAIM_CUSTOMER_EMAIL, accountId: COMPUTE_CLAIM_TARGET.accountId },
    launchOperationId: COMPUTE_CLAIM_TARGET.launchOperationId,
    workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
    resources: {
      computeAllocationId: COMPUTE_CLAIM_TARGET.computeAllocationId,
      storageId: COMPUTE_CLAIM_TARGET.storageId,
      attachmentId: "attachment-compute-claim-fixture",
      runtimeId: computeClaimRecoveryResources().runtimeId,
      receiptId: "receipt-compute-claim-fixture",
      workspaceApiKeyId: "42",
      runtimeServiceName: "workspace-service-compute-claim-fixture",
      workspaceUrl: `https://workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/`
    },
    expectedModel: "claude-sonnet-4-20250514",
    modelRequestKey: "recovered-workspace-model-fixture",
    allowedWrites: ["control_plane_e2e_attempt_reservation", "single_workspace_model_request", "control_plane_e2e_attempt_completion"],
    forbiddenWrites: ["launch", "debit", "recharge", "refund", "scale", "create_cvm", "create_cbs", "tencent", "kubernetes"]
  };
  const blocked = structuredClone(base.continuationEvidence);
  blocked.status = "blocked";
  blocked.errorCode = "compute_claim_continuation_failed";
  await assert.rejects(() => productionLiveQa.verifyRecoveredWorkspaceE2E({
    ...base,
    approvalJson: JSON.stringify(approval),
    continuationEvidence: blocked
  }), /recovered_workspace_e2e_resource_closure_required/);
  assert.equal(networkCalls, 0);

});

function recoveredWorkspaceE2EApprovalFixture(overrides = {}) {
  const resources = computeClaimRecoveryResources();
  return {
    schemaVersion: 1,
    approvalId: "approval-recovered-e2e-fixture",
    expiresAt: "2099-08-28T00:00:00Z",
    confirmation: "CONFIRM_SINGLE_MODEL_REQUEST_FOR_RECOVERED_WORKSPACE",
    mergedMainSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
	workspaceImageDigest: COMPUTE_CLAIM_WORKSPACE_DIGEST,
	recoveryApprovalId: "approval-compute-claim-fixture",
	recoveryApprovalDigest: computeClaimApprovalDigestForTest(computeClaimApprovalJson()),
	recoveryBindingDigest: "b".repeat(64),
	recoveryKey: "compute-claim-recovery-fixture",
    customer: { email: COMPUTE_CLAIM_CUSTOMER_EMAIL, accountId: COMPUTE_CLAIM_TARGET.accountId },
    launchOperationId: COMPUTE_CLAIM_TARGET.launchOperationId,
    workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
    resources: {
      computeAllocationId: COMPUTE_CLAIM_TARGET.computeAllocationId,
      storageId: COMPUTE_CLAIM_TARGET.storageId,
      attachmentId: resources.attachmentId,
      runtimeId: resources.runtimeId,
      receiptId: "receipt-compute-claim-fixture",
      workspaceApiKeyId: "42",
      runtimeServiceName: "workspace-service-compute-claim-fixture",
      workspaceUrl: `https://workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/`
    },
    expectedModel: "claude-sonnet-4-20250514",
    modelRequestKey: "recovered-workspace-model-fixture",
    allowedWrites: ["control_plane_e2e_attempt_reservation", "single_workspace_model_request", "control_plane_e2e_attempt_completion"],
    forbiddenWrites: ["launch", "debit", "recharge", "refund", "scale", "create_cvm", "create_cbs", "tencent", "kubernetes"],
    ...overrides
  };
}

function recoveredWorkspaceE2EContinuationFixture(overrides = {}) {
	const resources = computeClaimRecoveryResources();
	const approval = recoveredWorkspaceE2EApprovalFixture();
	return {
    schemaVersion: 2,
    operationMode: "compute_claim_recover_continuation",
    status: "succeeded",
    recoveryEligible: true,
    errorCode: "none",
    release: { mergedSha: BASIC_CANARY_MERGED_SHA, cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST },
	  handoff: {
		launchOperationId: COMPUTE_CLAIM_TARGET.launchOperationId,
		accountId: COMPUTE_CLAIM_TARGET.accountId,
		workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
		workspaceImageDigest: COMPUTE_CLAIM_WORKSPACE_DIGEST,
		recoveryApprovalId: approval.recoveryApprovalId,
		recoveryApprovalDigest: approval.recoveryApprovalDigest,
		recoveryKey: approval.recoveryKey,
		recoveryBindingDigest: approval.recoveryBindingDigest,
		resources: { ...approval.resources },
		terminalEvidence: {
		  workspacePodImageID: `containerd://${COMPUTE_CLAIM_WORKSPACE_DIGEST}`,
		  workspaceUrlHttpStatus: 200
		}
	  },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    backgroundMutationCountsState: "unknown",
    verifiedAt: "2026-08-28T00:00:00.000Z",
    ...overrides
  };
}

function recoveredWorkspaceE2EFixture({ markerExists = false, loseReserveResponse = false, usageStuck = false, browserResponseSuffix = "", frames = true } = {}) {
  const calls = [];
  const state = { markerReserved: markerExists, markerCompleted: false, reserveResponseLost: false, browserStarts: 0, modelRequests: 0 };
  const approval = recoveredWorkspaceE2EApprovalFixture();
  const continuationEvidence = recoveredWorkspaceE2EContinuationFixture();
  const usageRecord = {
    apiKeyId: "42",
    requestId: "req-recovered-workspace-e2e-1",
    createdAt: "2026-08-28T00:00:01Z",
    model: approval.expectedModel,
    inboundEndpoint: "/v1/responses",
    requestType: "sync",
    inputTokens: 8,
    outputTokens: 1,
    cacheCreationTokens: 0,
    cacheReadTokens: 0,
    actualCostUsdMicros: 120
  };
  const baselineUsage = [{
    ...usageRecord,
    requestId: "req-before-recovered-workspace-e2e",
    createdAt: "2026-08-27T00:00:00Z",
    inputTokens: 10,
    outputTokens: 5,
    actualCostUsdMicros: 100
  }];
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    const headers = new Headers(init.headers);
    calls.push({ method, path: url.pathname, search: url.search, body: init.body ? JSON.parse(init.body) : null });
    if (url.pathname === "/api/auth/login") {
      return json({ user: { accountId: COMPUTE_CLAIM_TARGET.accountId, role: "owner" } }, 200, {
        "set-cookie": "opl_session=recovered-e2e; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-recovered-e2e"
      });
    }
    assert.match(headers.get("cookie") || "", /opl_session=recovered-e2e/);
    if (url.pathname === "/api/auth/me") return source({
      consoleUserId: "usr-compute-claim-fixture",
      accountId: COMPUTE_CLAIM_TARGET.accountId,
      sub2apiUserId: "42",
      email: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      role: "owner",
      status: "active"
    }, "sub2api");
	if (url.pathname === `/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/runtime-status`) return source({
	  workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
	  runtimeId: continuationEvidence.handoff.resources.runtimeId,
	  serviceName: continuationEvidence.handoff.resources.runtimeServiceName,
	  status: "running",
	  ready: true,
	  url: continuationEvidence.handoff.resources.workspaceUrl,
      access: { username: "opl", credentialStatus: "configured" }
    }, "fabric");
    if (url.pathname === `/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/runtime-credentials/reveal`) {
      assert.equal(method, "POST");
      assert.equal(headers.get("x-opl-csrf"), "csrf-recovered-e2e");
      return json({
        workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
        access: { username: "opl", password: "workspace-password", credentialStatus: "configured" }
      }, 200, { "cache-control": "private, no-store" });
    }
    if (url.pathname === "/api/gateway/keys") return source({
      items: [
        { id: "7", kind: "general", name: "general-key", status: "active" },
        { id: "42", kind: "workspace", name: `opl-workspace-${stableCanaryId(COMPUTE_CLAIM_TARGET.workspaceId).slice(0, 12)}`, status: "active" }
      ],
      total: 2,
      page: 1,
      pageSize: 20,
      pages: 1
    });
    if (url.pathname === "/api/gateway/wallet") return source({
      userId: "42",
      currency: "USD",
      usdMicros: String(500_000_000 - (state.modelRequests > 0 && !usageStuck ? usageRecord.actualCostUsdMicros : 0)),
      status: "active"
    });
    if (url.pathname === "/api/gateway/keys/42/usage") {
      const items = state.modelRequests > 0 && !usageStuck ? [usageRecord, ...baselineUsage] : baselineUsage;
      return source({ items, total: items.length, page: 1, pageSize: 100, pages: 1 });
    }
    if (url.pathname === "/api/gateway/keys/42/usage-summary") {
      const includeRequest = state.modelRequests > 0 && !usageStuck;
      return source({
        totalRequests: 1 + (includeRequest ? 1 : 0),
        totalInputTokens: 10 + (includeRequest ? usageRecord.inputTokens : 0),
        totalOutputTokens: 5 + (includeRequest ? usageRecord.outputTokens : 0),
        totalTokens: 15 + (includeRequest ? usageRecord.inputTokens + usageRecord.outputTokens : 0),
        totalActualCostUsdMicros: 100 + (includeRequest ? usageRecord.actualCostUsdMicros : 0)
      });
    }
    if (url.pathname === `/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/recovered-e2e-attempt`) {
      assert.equal(method, "POST");
      if (state.markerReserved) return json({ error: "model_result_unknown" }, 409);
      state.markerReserved = true;
      if (loseReserveResponse && !state.reserveResponseLost) {
        state.reserveResponseLost = true;
        throw new Error("simulated_recovered_e2e_reserve_response_lost");
      }
      return json({ attemptId: "production-e2e-recovered-fixture", status: "attempted", approvalDigest: computeClaimApprovalDigestForTest(JSON.stringify(approval)) }, 201, {
        "cache-control": "private, no-store"
      });
    }
    if (url.pathname === `/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/recovered-e2e-attempt/complete`) {
      assert.equal(method, "POST");
      assert.equal(state.markerReserved, true);
      state.markerCompleted = true;
      return json({ attemptId: "production-e2e-recovered-fixture", status: "passed", approvalDigest: computeClaimApprovalDigestForTest(JSON.stringify(approval)) }, 200, {
        "cache-control": "private, no-store"
      });
    }
    return json({ error: "not_found" }, 404);
  };
  const createBrowser = browserFactory(state, { responseSuffix: browserResponseSuffix, frames });
  return {
    approval,
    continuationEvidence,
    calls,
    state,
    fetchImpl,
    browserFactory: async () => {
      state.browserStarts += 1;
      return createBrowser();
    }
  };
}

function recoveredWorkspaceE2EOptions(fixture) {
  return {
    origin: "https://cloud.medopl.cn",
    mergedSha: BASIC_CANARY_MERGED_SHA,
    customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
    customerPassword: "customer-password-fixture",
    approvalJson: JSON.stringify(fixture.approval),
    approvalId: fixture.approval.approvalId,
    confirmation: fixture.approval.confirmation,
    continuationEvidence: fixture.continuationEvidence,
    usageAttempts: 2,
    usageRetryDelayMs: 0,
    browserTimeoutMs: 20,
    modelTimeoutMs: 20,
    requestTimeoutMs: 20,
    fetchImpl: fixture.fetchImpl,
    browserFactory: fixture.browserFactory,
    now: new Date("2026-08-28T00:00:00Z")
  };
}

test("recovered Workspace E2E binds approval to the exact workflow main SHA before network access", async () => {
  const fixture = recoveredWorkspaceE2EFixture();
  await assert.rejects(() => productionLiveQa.verifyRecoveredWorkspaceE2E({
    ...recoveredWorkspaceE2EOptions(fixture),
    mergedSha: "d".repeat(40)
  }), /recovered_workspace_e2e_approval_invalid/);
  assert.equal(fixture.calls.length, 0);
  assert.equal(fixture.state.browserStarts, 0);
});

test("recovered Workspace E2E reserves once, sends one model request, proves Usage and balance, then completes", async () => {
  const fixture = recoveredWorkspaceE2EFixture();
  const result = await productionLiveQa.verifyRecoveredWorkspaceE2E(recoveredWorkspaceE2EOptions(fixture));

  assert.equal(result.status, "passed");
  assert.equal(result.usage.request.requestId, "req-recovered-workspace-e2e-1");
  assert.equal(result.usage.stats.delta.totalRequests, 1);
  assert.equal(BigInt(result.balance.before.usdMicros) - BigInt(result.balance.after.usdMicros), 120n);
  assert.deepEqual(result.writeCounts, {
    controlPlaneE2EAttemptReservations: 1,
    modelRequests: 1,
    controlPlaneE2EAttemptCompletions: 1,
    workspaceLaunches: 0,
    workspacePurchaseDebits: 0,
    walletAdjustments: 0,
    tencentMutations: 0,
    kubernetesMutations: 0
  });
  assert.equal(fixture.state.browserStarts, 1);
  assert.equal(fixture.state.modelRequests, 1);
  assert.equal(fixture.state.markerCompleted, true);
  assert.equal(fixture.calls.some(({ path }) => /workspace-launches|wallet-adjustments|compute-allocations|storage-volumes/.test(path)), false);
  assert.equal(fixture.calls.findIndex(({ path }) => path.endsWith("/recovered-e2e-attempt")) < fixture.calls.findIndex(({ path }) => path.endsWith("/recovered-e2e-attempt/complete")), true);
});

test("recovered Workspace E2E does not reserve the model marker before login and WebSocket preconditions pass", async () => {
  const fixture = recoveredWorkspaceE2EFixture({ frames: false });
  await assert.rejects(() => productionLiveQa.verifyRecoveredWorkspaceE2E(recoveredWorkspaceE2EOptions(fixture)), /workspace_websocket_frames_required/);
  assert.equal(fixture.state.browserStarts, 1);
  assert.equal(fixture.state.markerReserved, false);
  assert.equal(fixture.state.modelRequests, 0);
});

test("recovered Workspace E2E never resends when the persistent marker already exists", async () => {
  const fixture = recoveredWorkspaceE2EFixture({ markerExists: true });
  await assert.rejects(() => productionLiveQa.verifyRecoveredWorkspaceE2E(recoveredWorkspaceE2EOptions(fixture)), /model_result_unknown/);
  assert.equal(fixture.state.browserStarts, 1);
  assert.equal(fixture.state.modelRequests, 0);
});

test("recovered Workspace E2E never sends a model request after an unknown reserve response", async () => {
  const fixture = recoveredWorkspaceE2EFixture({ loseReserveResponse: true });
  await assert.rejects(() => productionLiveQa.verifyRecoveredWorkspaceE2E(recoveredWorkspaceE2EOptions(fixture)), /simulated_recovered_e2e_reserve_response_lost/);
  await assert.rejects(() => productionLiveQa.verifyRecoveredWorkspaceE2E(recoveredWorkspaceE2EOptions(fixture)), /model_result_unknown/);
  assert.equal(fixture.state.browserStarts, 2);
  assert.equal(fixture.state.modelRequests, 0);
});

test("recovered Workspace E2E never resends after a model request has an unknown result", async () => {
  const fixture = recoveredWorkspaceE2EFixture({ browserResponseSuffix: " unexpected" });
  await assert.rejects(() => productionLiveQa.verifyRecoveredWorkspaceE2E(recoveredWorkspaceE2EOptions(fixture)), /workspace_model_response_required/);
  const callsBeforeRetry = fixture.calls.length;
  await assert.rejects(() => productionLiveQa.verifyRecoveredWorkspaceE2E(recoveredWorkspaceE2EOptions(fixture)), /model_result_unknown/);
  assert.equal(fixture.state.browserStarts, 2);
  assert.equal(fixture.state.modelRequests, 1);
  assert.equal(fixture.state.markerCompleted, false);
  assert.equal(fixture.calls.slice(callsBeforeRetry).some(({ path }) => path.endsWith("/recovered-e2e-attempt/complete")), false);
});

test("recovered Workspace E2E CLI consumes only an absolute continuation artifact and the independent model approval", async (t) => {
  const fixture = recoveredWorkspaceE2EFixture();
  const directory = await mkdtemp(join(tmpdir(), "opl-recovered-workspace-e2e-cli-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const continuationPath = join(directory, "workspace-launch-continuation.json");
  await writeFile(continuationPath, `${JSON.stringify(fixture.continuationEvidence)}\n`, "utf8");
  let stdout = "";
  let stderr = "";

  const code = await runProductionLiveQaCli({
    argv: [
      "--recovered-workspace-e2e",
      "--allow-model-write",
      "--approval-id", fixture.approval.approvalId,
      "--continuation-evidence", continuationPath
    ],
    env: {
      OPL_MERGED_SHA: BASIC_CANARY_MERGED_SHA,
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_RECOVERED_WORKSPACE_CUSTOMER_EMAIL: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      OPL_RECOVERED_WORKSPACE_CUSTOMER_PASSWORD: "customer-password-fixture",
      OPL_RECOVERED_WORKSPACE_E2E_APPROVAL_JSON: JSON.stringify(fixture.approval),
      OPL_RECOVERED_WORKSPACE_E2E_CONFIRMATION: fixture.approval.confirmation,
      OPL_VERIFY_USAGE_ATTEMPTS: "2",
      OPL_VERIFY_USAGE_RETRY_DELAY_MS: "0",
      OPL_VERIFY_BROWSER_TIMEOUT_MS: "20",
      OPL_VERIFY_MODEL_TIMEOUT_MS: "20",
      OPL_VERIFY_REQUEST_TIMEOUT_MS: "20"
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: fixture.fetchImpl,
    browserFactory: fixture.browserFactory,
    now: new Date("2026-08-28T00:00:00Z")
  });

  assert.equal(code, 0, stderr);
  assert.equal(JSON.parse(stdout).operationMode, "recovered_workspace_e2e");
  assert.equal(fixture.state.modelRequests, 1);
  assert.equal(fixture.state.markerCompleted, true);
});

test("recovered Workspace E2E CLI rejects resource capabilities and invalid handoff before network access", async (t) => {
  const fixture = recoveredWorkspaceE2EFixture();
  const directory = await mkdtemp(join(tmpdir(), "opl-recovered-workspace-e2e-cli-guard-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const continuationPath = join(directory, "workspace-launch-continuation.json");
  await writeFile(continuationPath, `${JSON.stringify(fixture.continuationEvidence)}\n`, "utf8");
  const baseArgv = [
    "--recovered-workspace-e2e",
    "--allow-model-write",
    "--approval-id", fixture.approval.approvalId,
    "--continuation-evidence", continuationPath
  ];
  const baseEnv = {
    OPL_MERGED_SHA: BASIC_CANARY_MERGED_SHA,
    OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
    OPL_RECOVERED_WORKSPACE_CUSTOMER_EMAIL: COMPUTE_CLAIM_CUSTOMER_EMAIL,
    OPL_RECOVERED_WORKSPACE_CUSTOMER_PASSWORD: "customer-password-fixture",
    OPL_RECOVERED_WORKSPACE_E2E_APPROVAL_JSON: JSON.stringify(fixture.approval),
    OPL_RECOVERED_WORKSPACE_E2E_CONFIRMATION: fixture.approval.confirmation
  };

  for (const testCase of [
    { argv: [...baseArgv, "--allow-workspace-purchase"], env: baseEnv },
    { argv: baseArgv, env: { ...baseEnv, OPL_INTERNAL_SERVICE_TOKEN: "forbidden-capability" } },
    { argv: baseArgv, env: { ...baseEnv, TENCENT_DEPLOY_KUBECONFIG_PATH: "/run/secrets/kubeconfig" } },
    { argv: [...baseArgv.slice(0, -1), "relative-continuation.json"], env: baseEnv }
  ]) {
    let stderr = "";
    let calls = 0;
    const code = await runProductionLiveQaCli({
      argv: testCase.argv,
      env: testCase.env,
      stdout: { write: () => {} },
      stderr: { write: (chunk) => { stderr += chunk; } },
      fetchImpl: async () => { calls += 1; return json({}); },
      browserFactory: async () => { throw new Error("unexpected_browser_start"); },
      now: new Date("2026-08-28T00:00:00Z")
    });
    assert.equal(code, 1);
    assert.match(stderr, /recovered_workspace_e2e_cli_(?:conflict|continuation_evidence_invalid)/);
    assert.equal(calls, 0);
  }
});

function computeClaimCloudRevisionEvidence() {
  const service = (name, pod) => ({
    deployment: `opl-cloud-${name}`,
    deploymentUid: `${name}-deployment-uid`,
    revision: "1",
    replicaSet: `${name}-rs`,
    replicaSetUid: `${name}-rs-uid`,
    pod,
    podUid: `${pod}-uid`,
    imageID: `containerd://registry.example.test/opl-cloud@${BASIC_CANARY_CLOUD_DIGEST}`
  });
  return {
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudDigest: BASIC_CANARY_CLOUD_DIGEST,
    services: {
      controlPlane: service("control-plane", "opl-cloud-control-plane-current"),
      fabric: service("fabric", "opl-cloud-fabric-current"),
      ledger: service("ledger", "opl-cloud-ledger-current")
    }
  };
}

function computeClaimContinuationLaunch({ phase, status = "preparing", overrides = {} } = {}) {
  const terminal = status === "succeeded" && phase === "succeeded";
  return {
    operationId: COMPUTE_CLAIM_TARGET.launchOperationId,
    accountId: COMPUTE_CLAIM_TARGET.accountId,
    workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
    status,
    phase,
    packageId: COMPUTE_CLAIM_TARGET.packageId,
    sizeGb: 10,
    autoRenew: false,
    priceVersion: "pilot-usd-2026-07-v1",
    currency: "USD",
    totalChargeUsdMicros: 52580000,
    computeAllocationId: COMPUTE_CLAIM_TARGET.computeAllocationId,
    storageId: COMPUTE_CLAIM_TARGET.storageId,
    attachmentId: terminal ? "attachment-compute-claim-fixture" : "",
    workspaceApiKeyId: terminal ? "42" : "",
    receiptId: terminal ? "receipt-compute-claim-fixture" : "",
    runtimeServiceName: terminal ? "workspace-service-compute-claim-fixture" : "",
    url: terminal ? `https://workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/` : "",
    recovery: {
      approvalId: "approval-compute-claim-fixture",
      approvalDigest: computeClaimApprovalDigestForTest(computeClaimApprovalJson()),
      recoveryKey: "compute-claim-recovery-fixture",
      workspaceImageDigest: COMPUTE_CLAIM_WORKSPACE_DIGEST
    },
    errorCode: "",
    ...overrides
  };
}

function computeClaimRuntimeStatus(overrides = {}) {
  return source({
    workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
    runtimeId: "runtime-compute-claim-fixture",
    serviceName: "workspace-service-compute-claim-fixture",
    status: "running",
    ready: true,
    url: `https://workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/`,
    access: { username: "opl", credentialStatus: "configured" },
    ...overrides
  }, "fabric");
}

function computeClaimContinuationReceipt(overrides = {}) {
  return source({
    receiptId: "receipt-compute-claim-fixture",
    type: "billing.workspace_purchased.v1",
    status: "completed",
    workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
    totalUsdMicros: 52580000,
    components: {
      compute: { resourceType: "compute", resourceId: COMPUTE_CLAIM_TARGET.computeAllocationId, chargeUsdMicros: 50000000 },
      storage: { resourceType: "storage", resourceId: COMPUTE_CLAIM_TARGET.storageId, sizeGb: 10, chargeUsdMicros: 2580000 }
    },
    fulfillment: {
      computeAllocationId: COMPUTE_CLAIM_TARGET.computeAllocationId,
      storageId: COMPUTE_CLAIM_TARGET.storageId,
      attachmentId: "attachment-compute-claim-fixture",
      runtimeId: "runtime-compute-claim-fixture",
      workspaceApiKeyId: "42"
    },
    ...overrides
  }, "ledger");
}

test("compute-claim runner rejects Pro recovery before revision or Fabric access", async () => {
  let revisionReads = 0;
  let fabricReads = 0;
  await assert.rejects(() => productionLiveQa.diagnoseComputeClaimRecovery({
    target: COMPUTE_CLAIM_PRO_TARGET,
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => {
      revisionReads += 1;
      return computeClaimCloudRevisionEvidence();
    },
    execFileImpl: async () => {
      fabricReads += 1;
      return { stdout: JSON.stringify({ statusCode: 200, payload: computeClaimProof(), errorCode: "none" }) };
    }
  }), /compute_claim_recovery_target_invalid/);
  assert.equal(revisionReads, 0);
  assert.equal(fabricReads, 0);
});

test("compute-claim recovery rejects Pro before revision, login, or claim access", async () => {
  let revisionReads = 0;
  let fetchCalls = 0;
  await assert.rejects(() => productionLiveQa.recoverComputeClaim({
    target: COMPUTE_CLAIM_PRO_TARGET,
    approvalJson: computeClaimApprovalJson({ target: COMPUTE_CLAIM_PRO_TARGET }),
    approvalId: "approval-compute-claim-fixture",
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    internalServiceToken: "compute-claim-runner-capability",
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => {
      revisionReads += 1;
      return computeClaimCloudRevisionEvidence();
    },
    fetchImpl: async () => {
      fetchCalls += 1;
      return json({ error: "unexpected_request" }, 500);
    },
    now: new Date("2026-08-28T00:00:00Z")
  }), /compute_claim_recovery_target_invalid/);
  assert.equal(revisionReads, 0);
  assert.equal(fetchCalls, 0);
});

test("compute-claim diagnosis proves the exact compute identity through the current Ready Fabric Pod with zero mutation", async () => {
  assert.equal(typeof productionLiveQa.diagnoseComputeClaimRecovery, "function");
  const execCalls = [];
  const result = await productionLiveQa.diagnoseComputeClaimRecovery({
    target: COMPUTE_CLAIM_TARGET,
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    execFileImpl: async (command, args) => {
      execCalls.push({ command, args });
      return { stdout: JSON.stringify({ statusCode: 200, payload: computeClaimProof(), errorCode: "none" }) };
    }
  });

  assert.equal(result.schemaVersion, 2);
  assert.equal(result.operationMode, "compute_claim_diagnose");
  assert.equal(result.status, "proven");
  assert.equal(result.recoveryEligible, true);
  assert.equal(result.errorCode, "none");
  assert.deepEqual(result.runnerDirectMutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
  assert.deepEqual(result.proof.evidence, {
    cvm: { attempted: 0, confirmed: 0, unknown: 0, missing: [] },
    node: { attempted: 0, confirmed: 0, unknown: 0, missing: [] }
  });
  assert.equal(result.proof.nodeOwnershipState, "unallocated");
  assert.equal(result.proof.cvmOwnershipState, "recoverable");
  assert.equal(execCalls.length, 1);
  assert.equal(execCalls[0].command, "kubectl");
  assert.deepEqual(execCalls[0].args.slice(0, 7), ["--kubeconfig", "/run/secrets/kubeconfig", "-n", "opl-cloud", "exec", "opl-cloud-fabric-current", "-c"]);
  assert.match(execCalls[0].args.join(" "), /compute-claim-recovery\/proof/);
  assert.doesNotMatch(execCalls[0].args.join(" "), /compute-claim-recovery\/claim|internal-service-token/);
  assert.doesNotMatch(JSON.stringify(result), /providerRequestId|requestId|token|secret|password|raw/i);
});

test("compute-claim diagnosis binds one exact existing CBS without mutation", async () => {
  const result = await productionLiveQa.diagnoseComputeClaimRecovery({
    target: COMPUTE_CLAIM_TARGET,
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    execFileImpl: async () => ({ stdout: JSON.stringify({
      statusCode: 200,
      payload: computeClaimProof({ storageState: "storage_existing_exact", storageProviderResourceId: "disk-existing-fixture" }),
      errorCode: "none"
    }) })
  });

  assert.equal(result.status, "proven");
  assert.equal(result.recoveryEligible, true);
  assert.equal(result.proof.storageState, "storage_existing_exact");
  assert.equal(result.proof.storageProviderResourceId, "disk-existing-fixture");
  assert.deepEqual(result.runnerDirectMutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
});

test("compute-claim recovery binds exact existing CBS and forbids CreateDisks approval", async () => {
  const resources = computeClaimRecoveryResources(COMPUTE_CLAIM_TARGET, "storage_existing_exact", "disk-existing-fixture");
  const approvalJson = computeClaimApprovalJson({
    resources,
    allowedWrites: [...COMPUTE_CLAIM_EXISTING_STORAGE_ALLOWED_WRITES]
  });
  const calls = [];
  const result = await productionLiveQa.recoverComputeClaim({
    target: COMPUTE_CLAIM_TARGET,
    approvalJson,
    approvalId: "approval-compute-claim-fixture",
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    internalServiceToken: "compute-claim-runner-capability",
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl: async (input, init = {}) => {
      const url = new URL(String(input));
      const body = init.body ? JSON.parse(String(init.body)) : null;
      calls.push({ path: url.pathname, body });
      if (url.pathname === "/api/auth/login") {
        return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
          "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly",
          "x-opl-csrf-token": "csrf-compute-claim"
        });
      }
      if (url.pathname === "/api/operator/accounts") return computeClaimAccountAuthority();
      return json(computeClaimProof({
        storageState: "storage_existing_exact",
        storageProviderResourceId: "disk-existing-fixture",
        nodeOwnershipState: "target_owned",
        cvmOwnershipState: "target_owned"
      }));
    },
    now: new Date("2026-08-28T00:00:00Z")
  });

  const claim = calls.find(({ path }) => path.endsWith("/compute-claim-recovery/claim"));
  assert.equal(result.status, "claimed");
  assert.equal(result.proof.storageState, "storage_existing_exact");
  assert.equal(result.proof.storageProviderResourceId, "disk-existing-fixture");
  assert.deepEqual(claim.body.resources, resources);
  assert.deepEqual(claim.body.allowedWrites, COMPUTE_CLAIM_EXISTING_STORAGE_ALLOWED_WRITES);
  assert.equal(claim.body.allowedWrites.includes("create_original_cbs"), false);
});

test("compute-claim diagnosis preserves classified failure and zero mutation", async () => {
  const result = await productionLiveQa.diagnoseComputeClaimRecovery({
    target: COMPUTE_CLAIM_TARGET,
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    execFileImpl: async () => ({ stdout: JSON.stringify({
      statusCode: 409,
      payload: computeClaimProof({ eligible: false, reason: "storage_already_started", storageState: "unknown" }),
      errorCode: "storage_already_started"
    }) })
  });
  assert.equal(result.status, "blocked");
  assert.equal(result.recoveryEligible, false);
  assert.equal(result.errorCode, "storage_already_started");
  assert.deepEqual(result.runnerDirectMutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
});

test("compute-claim blocked artifacts never project untrusted provider strings", async () => {
  const marker = "provider_private_error_payload";
  const result = await productionLiveQa.diagnoseComputeClaimRecovery({
    target: COMPUTE_CLAIM_TARGET,
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    execFileImpl: async () => ({ stdout: JSON.stringify({
      statusCode: 409,
      payload: computeClaimProof({ eligible: false, reason: marker, machineName: marker }),
      errorCode: marker
    }) })
  });

  assert.doesNotMatch(JSON.stringify(result), new RegExp(marker));
  assert.deepEqual(result, {
    schemaVersion: 2,
    operationMode: "compute_claim_diagnose",
    status: "blocked",
    recoveryEligible: false,
    errorCode: "provider_describe",
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
  });
});

test("compute-claim runner rejects mutation evidence fields outside the CVM and Node allowlists", async () => {
  const marker = "ghp_secret";
  const result = await productionLiveQa.diagnoseComputeClaimRecovery({
    target: COMPUTE_CLAIM_TARGET,
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    execFileImpl: async () => ({ stdout: JSON.stringify({
      statusCode: 409,
      payload: computeClaimProof({
        eligible: false,
        reason: "provider_describe",
        tencentMutationCount: 0,
        failureStage: "cvm_final_readback",
        providerErrorClass: "readback_mismatch",
        evidence: {
          cvm: { attempted: 0, confirmed: 0, unknown: 0, missing: [marker] },
          node: { attempted: 0, confirmed: 0, unknown: 0, missing: [] }
        }
      }),
      errorCode: "provider_describe"
    }) })
  });

  assert.equal(result.errorCode, "identity_mismatch");
  assert.doesNotMatch(JSON.stringify(result), new RegExp(marker));
});

test("compute-claim runner rejects omitted missing for unknown or unconfirmed Go evidence", async () => {
  for (const evidence of [
    { attempted: 1, confirmed: 0, unknown: 1 },
    { attempted: 1, confirmed: 0, unknown: 0 },
    { attempted: 1, confirmed: 1, unknown: 1 }
  ]) {
    const result = await productionLiveQa.diagnoseComputeClaimRecovery({
      target: COMPUTE_CLAIM_TARGET,
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      kubeconfigPath: "/run/secrets/kubeconfig",
      namespace: "opl-cloud",
      cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
      execFileImpl: async () => ({ stdout: JSON.stringify({
        statusCode: 409,
        payload: computeClaimProof({
          eligible: false,
          reason: "provider_describe",
          tencentMutationCount: 1,
          failureStage: "cvm_final_readback",
          providerErrorClass: "readback_mismatch",
          evidence: { cvm: evidence, node: { attempted: 0, confirmed: 0, unknown: 0 } }
        }),
        errorCode: "provider_describe"
      }) })
    });
    assert.equal(result.schemaVersion, 2);
    assert.equal(result.status, "blocked");
    assert.equal(result.recoveryEligible, false);
    assert.equal(result.errorCode, "identity_mismatch");
    assert.deepEqual(result, {
      schemaVersion: 2,
      operationMode: "compute_claim_diagnose",
      status: "blocked",
      recoveryEligible: false,
      errorCode: "identity_mismatch",
      runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
    });
  }
});

test("compute-claim recovery requires an exact release approval and calls only the proof-gated claim route", async () => {
  const calls = [];
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    const headers = new Headers(init.headers);
    const body = init.body ? JSON.parse(String(init.body)) : null;
    calls.push({ method, path: url.pathname, headers, body });
    if (url.pathname === "/api/auth/login") {
      return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
        "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-compute-claim"
      });
    }
    if (url.pathname === "/api/operator/accounts") return computeClaimAccountAuthority();
    if (url.pathname.endsWith("/compute-claim-recovery/claim")) {
      return json(computeClaimProof({
        nodeOwnershipState: "target_owned",
        cvmOwnershipState: "target_owned",
        tencentMutationCount: 1,
        kubernetesMutationCount: 1,
        evidence: {
          cvm: { attempted: 1, confirmed: 1, unknown: 0 },
          node: { attempted: 1, confirmed: 1, unknown: 0 }
        }
      }));
    }
    return json({ error: "not_found" }, 404);
  };
  const recoverInput = {
    target: COMPUTE_CLAIM_TARGET,
    approvalJson: computeClaimApprovalJson(),
    approvalId: "approval-compute-claim-fixture",
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    internalServiceToken: "compute-claim-runner-capability",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl,
    now: new Date("2026-08-28T00:00:00Z")
  };
  const result = await productionLiveQa.recoverComputeClaim(recoverInput);
  const changedApprovalJson = computeClaimApprovalJson({ idempotencyKey: "compute-claim-http-other" });
  const changedApproval = await productionLiveQa.recoverComputeClaim({ ...recoverInput, approvalJson: changedApprovalJson });

  assert.equal(result.operationMode, "compute_claim_recover");
  assert.equal(result.schemaVersion, 2);
  assert.equal(result.status, "claimed");
  assert.deepEqual(result.approval, {
    approvalId: "approval-compute-claim-fixture",
    approvalDigest: computeClaimApprovalDigestForTest(computeClaimApprovalJson())
  });
  assert.equal(changedApproval.approval.approvalId, result.approval.approvalId);
  assert.equal(changedApproval.approval.approvalDigest, computeClaimApprovalDigestForTest(changedApprovalJson));
  assert.notEqual(changedApproval.approval.approvalDigest, result.approval.approvalDigest);
  assert.deepEqual(result.runnerDirectMutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
  assert.equal(result.proof.tencentMutationCount, 1);
  assert.equal(result.proof.kubernetesMutationCount, 1);
  assert.deepEqual(result.proof.evidence, {
    cvm: { attempted: 1, confirmed: 1, unknown: 0, missing: [] },
    node: { attempted: 1, confirmed: 1, unknown: 0, missing: [] }
  });
  assert.deepEqual(calls.map(({ method, path }) => [method, path]), [
    ["POST", "/api/auth/login"],
    ["GET", "/api/operator/accounts"],
    ["POST", `/api/operator/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}/compute-claim-recovery/claim`],
    ["POST", "/api/auth/login"],
    ["GET", "/api/operator/accounts"],
    ["POST", `/api/operator/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}/compute-claim-recovery/claim`]
  ]);
  assert.equal(calls[2].body.nodeName, COMPUTE_CLAIM_TARGET.nodeName);
  assert.equal(calls[2].body.privateIp, COMPUTE_CLAIM_TARGET.privateIp);
  assert.notEqual(calls[2].body.nodeName, calls[2].body.privateIp);
  assert.equal(calls[2].body.approvalDigest, computeClaimApprovalDigestForTest(computeClaimApprovalJson()));
  assert.equal(calls[2].body.workspaceImageDigest, COMPUTE_CLAIM_WORKSPACE_DIGEST);
  assert.equal(calls[2].body.customerEmail, COMPUTE_CLAIM_CUSTOMER_EMAIL);
  assert.equal(calls[2].body.recoveryKey, "compute-claim-recovery-fixture");
  assert.deepEqual(calls[2].body.resources, computeClaimRecoveryResources());
  assert.deepEqual(calls[2].body.attemptLimits, JSON.parse(computeClaimApprovalJson()).attemptLimits);
  assert.deepEqual(calls[2].body.allowedWrites, COMPUTE_CLAIM_ALLOWED_WRITES);
  assert.deepEqual(calls[2].body.forbiddenWrites, COMPUTE_CLAIM_FORBIDDEN_WRITES);
  assert.equal(calls[2].headers.get("idempotency-key"), "compute-claim-http-fixture");
  assert.equal(calls[5].headers.get("idempotency-key"), "compute-claim-http-other");
  assert.equal(calls[2].headers.get("x-opl-compute-claim-capability"), "compute-claim-runner-capability");
  assert.equal(calls.filter(({ path }) => path.endsWith("/claim")).every(({ headers }) => headers.get("x-opl-csrf") === "csrf-compute-claim"), true);
  assert.equal(calls.some(({ path }) => /wallet|refund|compute-allocations|storage-volumes/.test(path)), false);
});

test("compute-claim recovery lets the server decide an expired exact persisted replay", async () => {
  const expiresAt = "2026-08-27T00:00:00Z";
  const calls = [];
  const result = await productionLiveQa.recoverComputeClaim({
    target: COMPUTE_CLAIM_TARGET,
    approvalJson: computeClaimApprovalJson({ expiresAt }),
    approvalId: "approval-compute-claim-fixture",
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    internalServiceToken: "compute-claim-runner-capability",
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl: async (input, init = {}) => {
      const url = new URL(String(input));
      calls.push({ method: init.method || "GET", path: url.pathname, body: init.body ? JSON.parse(String(init.body)) : null });
      if (url.pathname === "/api/auth/login") {
        return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
          "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly",
          "x-opl-csrf-token": "csrf-compute-claim"
        });
      }
      if (url.pathname === "/api/operator/accounts") return computeClaimAccountAuthority();
      return json(computeClaimProof({ nodeOwnershipState: "target_owned", cvmOwnershipState: "target_owned" }));
    },
    now: new Date("2026-08-28T00:00:00Z")
  });

  assert.equal(result.status, "claimed");
  assert.deepEqual(calls.map(({ method, path }) => [method, path]), [
    ["POST", "/api/auth/login"],
    ["GET", "/api/operator/accounts"],
    ["POST", `/api/operator/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}/compute-claim-recovery/claim`]
  ]);
  assert.equal(calls[2].body.expiresAt, expiresAt);
});

test("compute-claim recovery never follows a claim redirect with the internal capability", async () => {
  const calls = [];
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const headers = new Headers(init.headers);
    calls.push({ origin: url.origin, path: url.pathname, redirect: init.redirect, headers });
    if (url.pathname === "/api/auth/login") {
      return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
        "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-compute-claim"
      });
    }
    if (url.pathname === "/api/operator/accounts") return computeClaimAccountAuthority();
    if (url.origin === "https://cloud.medopl.cn") {
      if (init.redirect === "manual") {
        return new Response(null, { status: 302, headers: { location: "https://redirect.example.test/claim" } });
      }
      return fetchImpl("https://redirect.example.test/claim", init);
    }
    return json(computeClaimProof({
      nodeOwnershipState: "target_owned",
      cvmOwnershipState: "target_owned"
    }));
  };

  const result = await productionLiveQa.recoverComputeClaim({
    target: COMPUTE_CLAIM_TARGET,
    approvalJson: computeClaimApprovalJson(),
    approvalId: "approval-compute-claim-fixture",
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    internalServiceToken: "compute-claim-runner-capability",
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl,
    now: new Date("2026-08-28T00:00:00Z")
  });

  assert.equal(result.status, "blocked");
  assert.equal(calls.filter(({ origin }) => origin === "https://redirect.example.test").length, 0);
  const claim = calls.find(({ path }) => path.endsWith("/compute-claim-recovery/claim"));
  assert.equal(claim.redirect, "manual");
  assert.equal(claim.headers.get("x-opl-compute-claim-capability"), "compute-claim-runner-capability");
});

test("compute-claim recovery rejects customer account drift before the claim POST", async () => {
  const calls = [];
  await assert.rejects(() => productionLiveQa.recoverComputeClaim({
    target: COMPUTE_CLAIM_TARGET,
    approvalJson: computeClaimApprovalJson(),
    approvalId: "approval-compute-claim-fixture",
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    internalServiceToken: "compute-claim-runner-capability",
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl: async (input) => {
      const url = new URL(String(input));
      calls.push(url.pathname);
      if (url.pathname === "/api/auth/login") {
        return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
          "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly",
          "x-opl-csrf-token": "csrf-compute-claim"
        });
      }
      if (url.pathname === "/api/operator/accounts") return computeClaimAccountAuthority({ email: "other@example.test" });
      return json(computeClaimProof({ nodeOwnershipState: "target_owned", cvmOwnershipState: "target_owned" }));
    },
    now: new Date("2026-08-28T00:00:00Z")
  }), /compute_claim_recovery_customer_identity_mismatch/);
  assert.deepEqual(calls, ["/api/auth/login", "/api/operator/accounts"]);
});

test("compute-claim continuation polls the same launch and runtime with no business writes", async () => {
  assert.equal(typeof productionLiveQa.continueComputeClaimWorkspace, "function");
  const calls = [];
  let launchReads = 0;
  const phases = [
    ["storage_fulfilling", "preparing"],
    ["attaching", "preparing"],
    ["secret_writing", "preparing"],
    ["runtime_starting", "preparing"],
    ["activating", "preparing"],
    ["receipt_pending", "preparing"],
    ["succeeded", "succeeded"]
  ];
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    calls.push({ method, path: url.pathname, body: init.body ? JSON.parse(String(init.body)) : null });
    if (url.pathname === "/api/auth/login") {
      return json({ user: { accountId: COMPUTE_CLAIM_TARGET.accountId, role: "owner" } }, 200, {
        "set-cookie": "opl_session=customer-fixture; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-customer"
      });
    }
    if (url.pathname === "/api/auth/me") return source({
      accountId: COMPUTE_CLAIM_TARGET.accountId,
      email: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      role: "owner",
      status: "active"
    }, "sub2api");
    if (url.pathname === `/api/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}`) {
      const [phase, status] = phases[Math.min(launchReads++, phases.length - 1)];
      return json(computeClaimContinuationLaunch({ phase, status }));
    }
    if (url.pathname === `/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/runtime-status`) return computeClaimRuntimeStatus();
    if (url.pathname === "/api/billing/receipts/receipt-compute-claim-fixture") return computeClaimContinuationReceipt();
    return json({ error: "not_found" }, 404);
  };

  const result = await productionLiveQa.continueComputeClaimWorkspace({
    target: COMPUTE_CLAIM_TARGET,
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
    customerPassword: "customer-password-fixture",
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    launchPollAttempts: 8,
    launchPollDelayMs: 0,
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    runtimePodEvidenceReader: async () => ({ imageID: `containerd://${COMPUTE_CLAIM_WORKSPACE_DIGEST}` }),
    workspaceUrlStatusReader: async () => 200,
    fetchImpl,
    now: new Date("2026-08-28T00:00:00Z")
  });

  assert.equal(result.operationMode, "compute_claim_recover_continuation");
  assert.equal(result.schemaVersion, 2);
  assert.equal(result.status, "succeeded");
  assert.equal(result.recoveryEligible, true);
  assert.equal(result.launch.status, "succeeded");
  assert.equal(result.launch.phase, "succeeded");
  assert.equal(result.launch.computeAllocationId, COMPUTE_CLAIM_TARGET.computeAllocationId);
  assert.equal(result.launch.storageId, COMPUTE_CLAIM_TARGET.storageId);
  assert.equal(result.launch.attachmentId, "attachment-compute-claim-fixture");
  assert.equal(result.launch.receiptId, "receipt-compute-claim-fixture");
  assert.equal(result.receipt.workspaceId, COMPUTE_CLAIM_TARGET.workspaceId);
  assert.equal(result.receipt.type, "billing.workspace_purchased.v1");
  assert.equal(result.receipt.components.storage.resourceId, COMPUTE_CLAIM_TARGET.storageId);
  assert.equal(result.receipt.components.storage.sizeGb, 10);
  assert.equal(result.receipt.fulfillment.attachmentId, result.launch.attachmentId);
  assert.equal(result.receipt.fulfillment.runtimeId, result.runtime.runtimeId);
  assert.equal(result.runtime.ready, true);
  assert.equal(result.runtime.status, "running");
  assert.equal(result.runtime.url, result.launch.url);
  assert.deepEqual(result.terminalEvidence, {
    workspacePodImageID: `containerd://${COMPUTE_CLAIM_WORKSPACE_DIGEST}`,
    workspaceUrlHttpStatus: 200
  });
  assert.deepEqual(result.recovery, {
    approvalId: "approval-compute-claim-fixture",
    approvalDigest: computeClaimApprovalDigestForTest(computeClaimApprovalJson()),
    recoveryKey: "compute-claim-recovery-fixture",
    workspaceImageDigest: COMPUTE_CLAIM_WORKSPACE_DIGEST
  });
  assert.deepEqual(result.runnerDirectMutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
  assert.equal(result.backgroundMutationCountsState, "unknown");
  assert.deepEqual(calls.map(({ method, path }) => [method, path]), [
    ["POST", "/api/auth/login"],
    ["GET", "/api/auth/me"],
    ...Array.from({ length: phases.length }, () => ["GET", `/api/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}`]),
    ["GET", `/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/runtime-status`],
    ["GET", "/api/billing/receipts/receipt-compute-claim-fixture"]
  ]);
  assert.equal(calls.filter(({ method, path }) => method === "POST" && /launch|claim|debit|wallet|storage/i.test(path)).length, 0);
  assert.doesNotMatch(JSON.stringify(result), /password|secret|token|cookie|providerRequestId/i);
});

test("workspace readback continuation accepts Pro product truth without changing the shared state machine", async () => {
  const target = COMPUTE_CLAIM_PRO_TARGET;
  const total = 240080000;
  const fetchImpl = async (input) => {
    const url = new URL(String(input));
    if (url.pathname === "/api/auth/login") {
      return json({ user: { accountId: target.accountId, role: "owner" } }, 200, {
        "set-cookie": "opl_session=customer-fixture; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-customer"
      });
    }
    if (url.pathname === "/api/auth/me") return source({
      accountId: target.accountId,
      email: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      role: "owner",
      status: "active"
    }, "sub2api");
    if (url.pathname === `/api/workspace-launches/${target.launchOperationId}`) return json(computeClaimContinuationLaunch({
      phase: "succeeded",
      status: "succeeded",
      overrides: { packageId: "pro", sizeGb: 100, totalChargeUsdMicros: total }
    }));
    if (url.pathname === `/api/workspaces/${target.workspaceId}/runtime-status`) return computeClaimRuntimeStatus();
    if (url.pathname === "/api/billing/receipts/receipt-compute-claim-fixture") return computeClaimContinuationReceipt({
      totalUsdMicros: total,
      components: {
        compute: { resourceType: "compute", resourceId: target.computeAllocationId, chargeUsdMicros: 230000000 },
        storage: { resourceType: "storage", resourceId: target.storageId, sizeGb: 100, chargeUsdMicros: 10080000 }
      }
    });
    return json({ error: "not_found" }, 404);
  };
  const result = await productionLiveQa.continueComputeClaimWorkspace({
    target,
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
    customerPassword: "customer-password-fixture",
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    launchPollAttempts: 1,
    launchPollDelayMs: 0,
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    runtimePodEvidenceReader: async (options) => {
      assert.equal(options.expectedCpu, 8);
      assert.equal(options.expectedMemoryGb, 16);
      assert.equal(options.expectedNodeName, target.nodeName);
      return { imageID: `containerd://${COMPUTE_CLAIM_WORKSPACE_DIGEST}` };
    },
    workspaceUrlStatusReader: async () => 200,
    fetchImpl
  });
  assert.equal(result.launch.packageId, "pro");
  assert.equal(result.launch.sizeGb, 100);
  assert.equal(result.receipt.components.storage.sizeGb, 100);
});

test("compute-claim continuation rejects non-public or noncanonical Workspace URLs", async () => {
  const invalidUrls = [
    `http://workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/`,
    `https://localhost/w/${COMPUTE_CLAIM_TARGET.workspaceId}/`,
    `https://10.20.30.40/w/${COMPUTE_CLAIM_TARGET.workspaceId}/`,
    `https://user:password@workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/`,
    `https://workspace.example.test/w/${COMPUTE_CLAIM_TARGET.workspaceId}/`,
    "https://workspace.medopl.cn/w/other/",
    `https://workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/?token=secret`,
    `https://workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/#fragment`,
    `https://workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/?`,
    `https://workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/#`,
    `https://workspace.medopl.cn:443/w/${COMPUTE_CLAIM_TARGET.workspaceId}/`,
    `https://:@workspace.medopl.cn/w/${COMPUTE_CLAIM_TARGET.workspaceId}/`
  ];
  for (const invalidUrl of invalidUrls) {
    const calls = [];
    const fetchImpl = async (input) => {
      const url = new URL(String(input));
      calls.push(url.pathname);
      if (url.pathname === "/api/auth/login") {
        return json({ user: { accountId: COMPUTE_CLAIM_TARGET.accountId, role: "owner" } }, 200, {
          "set-cookie": "opl_session=customer-fixture; Path=/; HttpOnly",
          "x-opl-csrf-token": "csrf-customer"
        });
      }
      if (url.pathname === "/api/auth/me") return source({
        accountId: COMPUTE_CLAIM_TARGET.accountId,
        email: COMPUTE_CLAIM_CUSTOMER_EMAIL,
        role: "owner",
        status: "active"
      }, "sub2api");
      if (url.pathname === `/api/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}`) {
        return json(computeClaimContinuationLaunch({
          phase: "succeeded",
          status: "succeeded",
          overrides: { url: invalidUrl }
        }));
      }
      if (url.pathname === `/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/runtime-status`) {
        return computeClaimRuntimeStatus({ url: invalidUrl });
      }
      if (url.pathname === "/api/billing/receipts/receipt-compute-claim-fixture") return computeClaimContinuationReceipt();
      return json({ error: "not_found" }, 404);
    };

    await assert.rejects(() => productionLiveQa.continueComputeClaimWorkspace({
      target: COMPUTE_CLAIM_TARGET,
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      origin: "https://cloud.medopl.cn",
      customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      customerPassword: "customer-password-fixture",
      kubeconfigPath: "/run/secrets/kubeconfig",
      namespace: "opl-cloud",
      launchPollAttempts: 1,
      launchPollDelayMs: 0,
      cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
      fetchImpl,
      now: new Date("2026-08-28T00:00:00Z")
    }), /compute_claim_continuation_workspace_url_invalid/, invalidUrl);
    assert.equal(calls.includes(`/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/runtime-status`), false, invalidUrl);
  }
});

test("compute-claim continuation fails closed on terminal error, identity drift, and runtime URL mismatch", async () => {
  const scenarios = [
    {
      name: "manual review",
      launch: () => computeClaimContinuationLaunch({ phase: "compute_claim_pending", status: "manual_review", overrides: { errorCode: "workspace_launch_manual_review" } }),
      expected: /compute_claim_continuation_manual_review/
    },
    {
      name: "identity drift",
      launch: (count) => computeClaimContinuationLaunch({ phase: count === 0 ? "storage_fulfilling" : "succeeded", status: count === 0 ? "preparing" : "succeeded", overrides: count === 0 ? {} : { storageId: "vol-other" } }),
      expected: /compute_claim_continuation_identity_mismatch/
    },
    {
      name: "unexpected launch error code",
      launch: () => computeClaimContinuationLaunch({ phase: "storage_fulfilling", status: "preparing", overrides: { errorCode: "workspace_launch_provider_error" } }),
      expected: /compute_claim_continuation_error_code/
    },
    {
      name: "runtime URL mismatch",
      launch: () => computeClaimContinuationLaunch({ phase: "succeeded", status: "succeeded" }),
      runtime: () => computeClaimRuntimeStatus({ url: "https://workspace.medopl.cn/w/other/" }),
      expected: /compute_claim_continuation_runtime_invalid/
    }
  ];
  for (const scenario of scenarios) {
    let launchReads = 0;
    const calls = [];
    const fetchImpl = async (input, init = {}) => {
      const url = new URL(String(input));
      const method = init.method || "GET";
      calls.push({ method, path: url.pathname });
      if (url.pathname === "/api/auth/login") {
        return json({ user: { accountId: COMPUTE_CLAIM_TARGET.accountId, role: "owner" } }, 200, {
          "set-cookie": "opl_session=customer-fixture; Path=/; HttpOnly",
          "x-opl-csrf-token": "csrf-customer"
        });
      }
      if (url.pathname === "/api/auth/me") return source({
        accountId: COMPUTE_CLAIM_TARGET.accountId,
        email: COMPUTE_CLAIM_CUSTOMER_EMAIL,
        role: "owner",
        status: "active"
      }, "sub2api");
      if (url.pathname === `/api/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}`) return json(scenario.launch(launchReads++));
      if (url.pathname === `/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/runtime-status`) return scenario.runtime ? scenario.runtime() : computeClaimRuntimeStatus();
      if (url.pathname === "/api/billing/receipts/receipt-compute-claim-fixture") return computeClaimContinuationReceipt();
      return json({ error: "not_found" }, 404);
    };
    await assert.rejects(() => productionLiveQa.continueComputeClaimWorkspace({
      target: COMPUTE_CLAIM_TARGET,
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      origin: "https://cloud.medopl.cn",
      customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      customerPassword: "customer-password-fixture",
      kubeconfigPath: "/run/secrets/kubeconfig",
      namespace: "opl-cloud",
      launchPollAttempts: 3,
      launchPollDelayMs: 0,
      cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
      fetchImpl,
      now: new Date("2026-08-28T00:00:00Z")
    }), scenario.expected, scenario.name);
    assert.equal(calls.filter(({ method, path }) => method === "POST" && /launch|claim|debit|wallet|storage/i.test(path)).length, 0, scenario.name);
  }
});

test("compute-claim continuation rejects a mismatched purchase receipt without business writes", async () => {
  const calls = [];
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    calls.push({ method, path: url.pathname });
    if (url.pathname === "/api/auth/login") {
      return json({ user: { accountId: COMPUTE_CLAIM_TARGET.accountId, role: "owner" } }, 200, {
        "set-cookie": "opl_session=customer-fixture; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-customer"
      });
    }
    if (url.pathname === "/api/auth/me") return source({ accountId: COMPUTE_CLAIM_TARGET.accountId, email: COMPUTE_CLAIM_CUSTOMER_EMAIL, role: "owner", status: "active" }, "sub2api");
    if (url.pathname === `/api/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}`) return json(computeClaimContinuationLaunch({ phase: "succeeded", status: "succeeded" }));
    if (url.pathname === `/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/runtime-status`) return computeClaimRuntimeStatus();
    if (url.pathname === "/api/billing/receipts/receipt-compute-claim-fixture") return computeClaimContinuationReceipt({ components: { compute: { resourceType: "compute", resourceId: COMPUTE_CLAIM_TARGET.computeAllocationId, chargeUsdMicros: 50000000 }, storage: { resourceType: "storage", resourceId: COMPUTE_CLAIM_TARGET.storageId, sizeGb: 20, chargeUsdMicros: 2580000 } } });
    return json({ error: "not_found" }, 404);
  };
  await assert.rejects(() => productionLiveQa.continueComputeClaimWorkspace({
    target: COMPUTE_CLAIM_TARGET,
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
    customerPassword: "customer-password-fixture",
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    launchPollAttempts: 1,
    launchPollDelayMs: 0,
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl,
    now: new Date("2026-08-28T00:00:00Z")
  }), /compute_claim_continuation_receipt_invalid/);
  assert.equal(calls.filter(({ method, path }) => method === "POST" && /launch|claim|debit|wallet|storage/i.test(path)).length, 0);
});

test("compute-claim continuation rejects a receipt for another Workspace without business writes", async () => {
  const calls = [];
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    calls.push({ method, path: url.pathname });
    if (url.pathname === "/api/auth/login") {
      return json({ user: { accountId: COMPUTE_CLAIM_TARGET.accountId, role: "owner" } }, 200, {
        "set-cookie": "opl_session=customer-fixture; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-customer"
      });
    }
    if (url.pathname === "/api/auth/me") return source({ accountId: COMPUTE_CLAIM_TARGET.accountId, email: COMPUTE_CLAIM_CUSTOMER_EMAIL, role: "owner", status: "active" }, "sub2api");
    if (url.pathname === `/api/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}`) return json(computeClaimContinuationLaunch({ phase: "succeeded", status: "succeeded" }));
    if (url.pathname === `/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/runtime-status`) return computeClaimRuntimeStatus();
    if (url.pathname === "/api/billing/receipts/receipt-compute-claim-fixture") return computeClaimContinuationReceipt({ workspaceId: "ws-other" });
    return json({ error: "not_found" }, 404);
  };
  await assert.rejects(() => productionLiveQa.continueComputeClaimWorkspace({
    target: COMPUTE_CLAIM_TARGET,
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    customerEmail: COMPUTE_CLAIM_CUSTOMER_EMAIL,
    customerPassword: "customer-password-fixture",
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    launchPollAttempts: 1,
    launchPollDelayMs: 0,
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl,
    now: new Date("2026-08-28T00:00:00Z")
  }), /compute_claim_continuation_receipt_invalid/);
  assert.equal(calls.filter(({ method, path }) => method === "POST" && /launch|claim|debit|wallet|storage/i.test(path)).length, 0);
});

test("compute-claim continuation CLI reads only the customer password environment secret", async () => {
  let stdout = "";
  let stderr = "";
  const calls = [];
  const code = await runProductionLiveQaCli({
    argv: ["--compute-claim-continue", "--compute-claim-target-json", JSON.stringify(COMPUTE_CLAIM_TARGET), "--launch-poll-delay-ms", "0"],
    env: {
      OPL_MERGED_SHA: BASIC_CANARY_MERGED_SHA,
      OPL_COMPUTE_CLAIM_CLOUD_DIGEST: BASIC_CANARY_CLOUD_DIGEST,
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_BASIC_CANARY_CUSTOMER_EMAIL: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      OPL_BASIC_CANARY_CUSTOMER_PASSWORD: "customer-password-fixture",
      OPL_K8S_NAMESPACE: "opl-cloud",
      KUBECONFIG: "/run/secrets/kubeconfig"
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    execFileImpl: async (command, args) => {
      if (command === "kubectl" && args.includes("get") && args.includes("pods")) {
        return { stdout: JSON.stringify({
          kind: "List",
          items: [{
            metadata: {
              name: "runtime-compute-claim-fixture",
              labels: { "oplcloud.cn/workspace-id": COMPUTE_CLAIM_TARGET.workspaceId },
              ownerReferences: [{
                apiVersion: "apps/v1",
                kind: "ReplicaSet",
                name: "runtime-compute-claim-fixture-rs",
                uid: "runtime-compute-claim-fixture-rs-uid",
                controller: true
              }]
            },
            spec: {
              nodeName: COMPUTE_CLAIM_TARGET.nodeName,
              containers: [{ name: "workspace", resources: { limits: { cpu: "2", memory: "4Gi" } } }]
            },
            status: {
              phase: "Running",
              conditions: [{ type: "Ready", status: "True" }],
              containerStatuses: [{
                name: "workspace",
                ready: true,
                imageID: `containerd://${COMPUTE_CLAIM_WORKSPACE_DIGEST}`
              }]
            }
          }]
        }) };
      }
      throw new Error(`unexpected_command:${command}:${args.join(" ")}`);
    },
    fetchImpl: async (input, init = {}) => {
      const url = new URL(String(input));
      if (url.hostname === "workspace.medopl.cn") return new Response("workspace ready", { status: 200 });
      const method = init.method || "GET";
      calls.push({ method, path: url.pathname });
      if (url.pathname === "/api/auth/login") {
        assert.deepEqual(JSON.parse(String(init.body)), { email: COMPUTE_CLAIM_CUSTOMER_EMAIL, password: "customer-password-fixture" });
        return json({ user: { accountId: COMPUTE_CLAIM_TARGET.accountId, role: "owner" } }, 200, {
          "set-cookie": "opl_session=customer-fixture; Path=/; HttpOnly",
          "x-opl-csrf-token": "csrf-customer"
        });
      }
      if (url.pathname === "/api/auth/me") return source({ accountId: COMPUTE_CLAIM_TARGET.accountId, email: COMPUTE_CLAIM_CUSTOMER_EMAIL, role: "owner", status: "active" }, "sub2api");
      if (url.pathname === `/api/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}`) return json(computeClaimContinuationLaunch({ phase: "succeeded", status: "succeeded" }));
      if (url.pathname === `/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/runtime-status`) return computeClaimRuntimeStatus();
      if (url.pathname === "/api/billing/receipts/receipt-compute-claim-fixture") return computeClaimContinuationReceipt();
      return json({ error: "not_found" }, 404);
    },
    now: new Date("2026-08-28T00:00:00Z")
  });

  assert.equal(code, 0, stderr);
  const result = JSON.parse(stdout);
  assert.equal(result.status, "succeeded");
  assert.deepEqual(calls.map(({ method, path }) => [method, path]), [
    ["POST", "/api/auth/login"],
    ["GET", "/api/auth/me"],
    ["GET", `/api/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}`],
    ["GET", `/api/workspaces/${COMPUTE_CLAIM_TARGET.workspaceId}/runtime-status`],
    ["GET", "/api/billing/receipts/receipt-compute-claim-fixture"]
  ]);
  assert.doesNotMatch(stdout, /customer-password-fixture|password|secret|token/i);
});

test("compute-claim recovery rejects missing runner capability and mutation counts above the hard bounds", async () => {
  let networkCalls = 0;
  let revisionCalls = 0;
  await assert.rejects(() => productionLiveQa.recoverComputeClaim({
    target: COMPUTE_CLAIM_TARGET,
    approvalJson: computeClaimApprovalJson(),
    approvalId: "approval-compute-claim-fixture",
    mergedSha: BASIC_CANARY_MERGED_SHA,
    cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
    origin: "https://cloud.medopl.cn",
    adminEmail: ADMIN_EMAIL,
    adminPassword: ADMIN_PASSWORD,
    kubeconfigPath: "/run/secrets/kubeconfig",
    namespace: "opl-cloud",
    cloudRevisionEvidenceReader: async () => { revisionCalls += 1; return computeClaimCloudRevisionEvidence(); },
    fetchImpl: async () => { networkCalls += 1; return json({}); },
    now: new Date("2026-08-28T00:00:00Z")
  }), /compute_claim_recovery_capability_required/);
  assert.equal(networkCalls, 0);
  assert.equal(revisionCalls, 0);

  for (const counts of [
    { tencentMutationCount: 6, kubernetesMutationCount: 1 },
    { tencentMutationCount: 5, kubernetesMutationCount: 2 },
    { sub2apiMutationCount: 1, tencentMutationCount: 0, kubernetesMutationCount: 0 }
  ]) {
    const fetchImpl = async (input, init = {}) => {
      const url = new URL(String(input));
      if (url.pathname === "/api/auth/login") {
        return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
          "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly",
          "x-opl-csrf-token": "csrf-compute-claim"
        });
      }
      if (url.pathname === "/api/operator/accounts") return computeClaimAccountAuthority();
      return json(computeClaimProof({
        nodeOwnershipState: "target_owned",
        cvmOwnershipState: "target_owned",
        ...counts
      }));
    };
    const result = await productionLiveQa.recoverComputeClaim({
      target: COMPUTE_CLAIM_TARGET,
      approvalJson: computeClaimApprovalJson(),
      approvalId: "approval-compute-claim-fixture",
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      origin: "https://cloud.medopl.cn",
      adminEmail: ADMIN_EMAIL,
      adminPassword: ADMIN_PASSWORD,
      internalServiceToken: "compute-claim-runner-capability",
      kubeconfigPath: "/run/secrets/kubeconfig",
      namespace: "opl-cloud",
      cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
      fetchImpl,
      now: new Date("2026-08-28T00:00:00Z")
    });
    assert.equal(result.status, "blocked");
    assert.equal(result.recoveryEligible, false);
    assert.equal(result.errorCode, "identity_mismatch");
  }
});

test("compute-claim recovery rejects missing or non-admin credentials before the claim POST", async () => {
  for (const credentials of [
    { adminEmail: ADMIN_EMAIL, adminPassword: "" },
    { adminEmail: "owner@example.com", adminPassword: ADMIN_PASSWORD }
  ]) {
    let networkCalls = 0;
    let revisionCalls = 0;
    await assert.rejects(() => productionLiveQa.recoverComputeClaim({
      target: COMPUTE_CLAIM_TARGET,
      approvalJson: computeClaimApprovalJson(),
      approvalId: "approval-compute-claim-fixture",
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      origin: "https://cloud.medopl.cn",
      ...credentials,
      internalServiceToken: "compute-claim-runner-capability",
      kubeconfigPath: "/run/secrets/kubeconfig",
      namespace: "opl-cloud",
      cloudRevisionEvidenceReader: async () => { revisionCalls += 1; return computeClaimCloudRevisionEvidence(); },
      fetchImpl: async () => { networkCalls += 1; return json({}); },
      now: new Date("2026-08-28T00:00:00Z")
    }), /existing_admin_verifier_credentials_unavailable/);
    assert.equal(networkCalls, 0);
    assert.equal(revisionCalls, 0);
  }
});

test("compute-claim recovery rejects approval or target drift before network or cluster access", async () => {
  for (const approvalJson of [
    computeClaimApprovalJson({ mergedMainSha: "d".repeat(40) }),
    computeClaimApprovalJson({ cloudImageDigest: `sha256:${"d".repeat(64)}` }),
    computeClaimApprovalJson({ target: { ...COMPUTE_CLAIM_TARGET, machineName: "machine-other" } })
  ]) {
    let fetchCalls = 0;
    let revisionCalls = 0;
    await assert.rejects(() => productionLiveQa.recoverComputeClaim({
      target: COMPUTE_CLAIM_TARGET,
      approvalJson,
      approvalId: "approval-compute-claim-fixture",
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      origin: "https://cloud.medopl.cn",
      adminEmail: ADMIN_EMAIL,
      adminPassword: ADMIN_PASSWORD,
      kubeconfigPath: "/run/secrets/kubeconfig",
      namespace: "opl-cloud",
      cloudRevisionEvidenceReader: async () => { revisionCalls += 1; return {}; },
      fetchImpl: async () => { fetchCalls += 1; return json({}); },
      now: new Date("2026-08-28T00:00:00Z")
    }), /compute_claim_recovery_approval_invalid/);
    assert.equal(fetchCalls, 0);
    assert.equal(revisionCalls, 0);
  }
});

test("compute-claim diagnosis CLI emits the same zero-mutation artifact", async () => {
  let stdout = "";
  let stderr = "";
  const code = await runProductionLiveQaCli({
    argv: ["--compute-claim-diagnose", "--compute-claim-target-json", JSON.stringify(COMPUTE_CLAIM_TARGET)],
    env: {
      OPL_MERGED_SHA: BASIC_CANARY_MERGED_SHA,
      OPL_COMPUTE_CLAIM_CLOUD_DIGEST: BASIC_CANARY_CLOUD_DIGEST,
      OPL_K8S_NAMESPACE: "opl-cloud",
      KUBECONFIG: "/run/secrets/kubeconfig"
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    execFileImpl: async () => ({ stdout: JSON.stringify({ statusCode: 200, payload: computeClaimProof(), errorCode: "none" }) })
  });
  assert.equal(code, 0, stderr);
  const result = JSON.parse(stdout);
  assert.equal(result.schemaVersion, 2);
  assert.equal(result.operationMode, "compute_claim_diagnose");
  assert.deepEqual(result.runnerDirectMutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
});

test("compute-claim approval validation CLI proves the server binding without claim mutation", async () => {
  let stdout = "";
  let stderr = "";
  const calls = [];
  const approval = JSON.parse(computeClaimApprovalJson());
  const approvalDigest = createHash("sha256").update(canonicalJsonForTest(approval)).digest("hex");
  const code = await runProductionLiveQaCli({
    argv: [
      "--compute-claim-validate",
      "--compute-claim-target-json", JSON.stringify(COMPUTE_CLAIM_TARGET),
      "--approval-id", approval.approvalId
    ],
    env: {
      OPL_MERGED_SHA: BASIC_CANARY_MERGED_SHA,
      OPL_COMPUTE_CLAIM_CLOUD_DIGEST: BASIC_CANARY_CLOUD_DIGEST,
      OPL_COMPUTE_CLAIM_RECOVERY_APPROVAL_JSON: JSON.stringify(approval),
      OPL_INTERNAL_SERVICE_TOKEN: "compute-claim-runner-capability",
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD,
      OPL_BASIC_CANARY_CUSTOMER_EMAIL: COMPUTE_CLAIM_CUSTOMER_EMAIL,
      OPL_K8S_NAMESPACE: "opl-cloud",
      KUBECONFIG: "/run/secrets/kubeconfig"
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
    fetchImpl: async (input, init = {}) => {
      const url = new URL(String(input));
      const headers = new Headers(init.headers);
      calls.push({ path: url.pathname, method: String(init.method || "GET"), headers });
      if (url.pathname === "/api/auth/login") {
        return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
          "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly",
          "x-opl-csrf-token": "csrf-compute-claim"
        });
      }
      if (url.pathname === "/api/operator/accounts") return computeClaimAccountAuthority();
      return json({
        schemaVersion: 2,
        status: "proven",
        approvalId: approval.approvalId,
        approvalDigest,
        launchOperationId: COMPUTE_CLAIM_TARGET.launchOperationId,
        accountId: COMPUTE_CLAIM_TARGET.accountId,
        workspaceId: COMPUTE_CLAIM_TARGET.workspaceId,
        runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
      });
    },
    now: new Date("2026-08-28T00:00:00Z")
  });

  assert.equal(code, 0, stderr);
  assert.deepEqual(JSON.parse(stdout), {
    schemaVersion: 2,
    operationMode: "compute_claim_validate",
    status: "proven",
    recoveryEligible: true,
    errorCode: "none",
    release: {
      mergedSha: BASIC_CANARY_MERGED_SHA,
      cloudImageDigest: BASIC_CANARY_CLOUD_DIGEST,
      revisions: { controlPlane: "1", fabric: "1", ledger: "1" }
    },
    target: COMPUTE_CLAIM_TARGET,
    approval: { approvalId: approval.approvalId, approvalDigest },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
  });
  assert.deepEqual(calls.map(({ path }) => path), [
    "/api/auth/login",
    "/api/operator/accounts",
    `/api/operator/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}/compute-claim-recovery/validate`
  ]);
  assert.equal(calls[2].method, "POST");
  assert.equal(calls[2].headers.get("x-opl-compute-claim-capability"), "compute-claim-runner-capability");
  assert.doesNotMatch(`${stdout}\n${stderr}`, /password|secret|token|cookie|customer@example\.com|compute-claim-runner-capability/i);
});

test("compute-claim approval validation rejects workflow customer drift before release or network access", async () => {
  let stdout = "";
  let releaseReads = 0;
  let fetchCalls = 0;
  const approval = JSON.parse(computeClaimApprovalJson());
  const code = await runProductionLiveQaCli({
    argv: [
      "--compute-claim-validate",
      "--compute-claim-target-json", JSON.stringify(COMPUTE_CLAIM_TARGET),
      "--approval-id", approval.approvalId
    ],
    env: {
      OPL_MERGED_SHA: BASIC_CANARY_MERGED_SHA,
      OPL_COMPUTE_CLAIM_CLOUD_DIGEST: BASIC_CANARY_CLOUD_DIGEST,
      OPL_COMPUTE_CLAIM_RECOVERY_APPROVAL_JSON: JSON.stringify(approval),
      OPL_INTERNAL_SERVICE_TOKEN: "compute-claim-runner-capability",
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD,
      OPL_BASIC_CANARY_CUSTOMER_EMAIL: "other@example.com",
      OPL_K8S_NAMESPACE: "opl-cloud",
      KUBECONFIG: "/run/secrets/kubeconfig"
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: () => {} },
    cloudRevisionEvidenceReader: async () => { releaseReads += 1; return computeClaimCloudRevisionEvidence(); },
    fetchImpl: async () => { fetchCalls += 1; return json({}); }
  });

  assert.equal(code, 1);
  assert.equal(JSON.parse(stdout).errorCode, "compute_claim_validation_customer_identity_mismatch");
  assert.equal(releaseReads, 0);
  assert.equal(fetchCalls, 0);
});

test("compute-claim recovery CLI forwards the internal runner capability", async () => {
	let stdout = "";
	let stderr = "";
	const calls = [];
	const code = await runProductionLiveQaCli({
		argv: [
			"--compute-claim-recover",
			"--compute-claim-target-json", JSON.stringify(COMPUTE_CLAIM_TARGET),
			"--approval-id", "approval-compute-claim-fixture"
		],
		env: {
			OPL_MERGED_SHA: BASIC_CANARY_MERGED_SHA,
			OPL_COMPUTE_CLAIM_CLOUD_DIGEST: BASIC_CANARY_CLOUD_DIGEST,
			OPL_COMPUTE_CLAIM_RECOVERY_APPROVAL_JSON: computeClaimApprovalJson(),
			OPL_INTERNAL_SERVICE_TOKEN: "compute-claim-runner-capability",
			OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
			OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
			OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD,
			OPL_K8S_NAMESPACE: "opl-cloud",
			KUBECONFIG: "/run/secrets/kubeconfig"
		},
		stdout: { write: (chunk) => { stdout += chunk; } },
		stderr: { write: (chunk) => { stderr += chunk; } },
		cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
		fetchImpl: async (input, init = {}) => {
			const url = new URL(String(input));
			const headers = new Headers(init.headers);
			calls.push({ path: url.pathname, headers });
			if (url.pathname === "/api/auth/login") {
				return json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
					"set-cookie": "opl_session=session-fixture; Path=/; HttpOnly",
					"x-opl-csrf-token": "csrf-compute-claim"
				});
			}
			if (url.pathname === "/api/operator/accounts") return computeClaimAccountAuthority();
			return json(computeClaimProof({
				nodeOwnershipState: "target_owned",
				cvmOwnershipState: "target_owned",
				tencentMutationCount: 1,
				kubernetesMutationCount: 1,
					evidence: {
						cvm: { attempted: 1, confirmed: 1, unknown: 0 },
						node: { attempted: 1, confirmed: 1, unknown: 0 }
					}
			}));
		},
		now: new Date("2026-08-28T00:00:00Z")
	});

	assert.equal(code, 0, stderr);
	const artifact = JSON.parse(stdout);
	assert.equal(artifact.schemaVersion, 2);
	assert.equal(artifact.status, "claimed");
	assert.deepEqual(artifact.runnerDirectMutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
	assert.deepEqual(calls.map(({ path }) => path), [
		"/api/auth/login",
		"/api/operator/accounts",
		`/api/operator/workspace-launches/${COMPUTE_CLAIM_TARGET.launchOperationId}/compute-claim-recovery/claim`
	]);
	assert.equal(calls[2].headers.get("x-opl-compute-claim-capability"), "compute-claim-runner-capability");
});

test("compute-claim recovery CLI classifies pre-claim failures without exposing raw errors", async () => {
  const marker = "raw-private-provider-marker";
  const argv = [
    "--compute-claim-recover",
    "--compute-claim-target-json", JSON.stringify(COMPUTE_CLAIM_TARGET),
    "--approval-id", "approval-compute-claim-fixture"
  ];
  const env = {
    OPL_MERGED_SHA: BASIC_CANARY_MERGED_SHA,
    OPL_COMPUTE_CLAIM_CLOUD_DIGEST: BASIC_CANARY_CLOUD_DIGEST,
    OPL_COMPUTE_CLAIM_RECOVERY_APPROVAL_JSON: computeClaimApprovalJson(),
    OPL_INTERNAL_SERVICE_TOKEN: "compute-claim-runner-capability",
    OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
    OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
    OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD,
    OPL_K8S_NAMESPACE: "opl-cloud",
    KUBECONFIG: "/run/secrets/kubeconfig"
  };
  const adminLogin = () => json({ user: { accountId: "acct-admin", role: "admin" } }, 200, {
    "set-cookie": "opl_session=session-fixture; Path=/; HttpOnly",
    "x-opl-csrf-token": "csrf-compute-claim"
  });
  const cases = [
    {
      name: "release readback",
      errorCode: "compute_claim_recovery_release_readback_failed",
      cloudRevisionEvidenceReader: async () => { throw new Error(marker); },
      fetchImpl: async () => { throw new Error("network_not_expected"); }
    },
    {
      name: "admin login",
      errorCode: "compute_claim_recovery_admin_login_failed",
      cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
      fetchImpl: async () => { throw new Error(marker); }
    },
    {
      name: "customer authority",
      errorCode: "compute_claim_recovery_customer_identity_mismatch",
      cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
      fetchImpl: async (input) => {
        const url = new URL(String(input));
        if (url.pathname === "/api/auth/login") return adminLogin();
        if (url.pathname === "/api/operator/accounts") return computeClaimAccountAuthority({ email: "other@example.test" });
        throw new Error("claim_not_expected");
      }
    },
    {
      name: "claim transport",
      errorCode: "compute_claim_recovery_claim_transport_failed",
      cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
      fetchImpl: async (input) => {
        const url = new URL(String(input));
        if (url.pathname === "/api/auth/login") return adminLogin();
        if (url.pathname === "/api/operator/accounts") return computeClaimAccountAuthority();
        throw new Error(marker);
      }
    },
    {
      name: "claim response",
      errorCode: "compute_claim_recovery_control_plane_response_invalid",
      cloudRevisionEvidenceReader: async () => computeClaimCloudRevisionEvidence(),
      fetchImpl: async (input) => {
        const url = new URL(String(input));
        if (url.pathname === "/api/auth/login") return adminLogin();
        if (url.pathname === "/api/operator/accounts") return computeClaimAccountAuthority();
        return new Response(marker, { status: 502 });
      }
    }
  ];

  for (const testCase of cases) {
    let stdout = "";
    let stderr = "";
    const code = await runProductionLiveQaCli({
      argv,
      env,
      stdout: { write: (chunk) => { stdout += chunk; } },
      stderr: { write: (chunk) => { stderr += chunk; } },
      cloudRevisionEvidenceReader: testCase.cloudRevisionEvidenceReader,
      fetchImpl: testCase.fetchImpl,
      now: new Date("2026-08-28T00:00:00Z")
    });
    assert.equal(code, 1, testCase.name);
    assert.deepEqual(JSON.parse(stdout), {
      schemaVersion: 2,
      operationMode: "compute_claim_recover",
      status: "blocked",
      recoveryEligible: false,
      errorCode: testCase.errorCode,
      runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
    }, testCase.name);
    assert.match(stderr, new RegExp(testCase.errorCode), testCase.name);
    assert.doesNotMatch(`${stdout}\n${stderr}`, new RegExp(`${marker}|${COMPUTE_CLAIM_CUSTOMER_EMAIL}|compute-claim-runner-capability|password|secret|token`, "i"), testCase.name);
  }
});

test("compute-claim recovery CLI rejects missing explicit approval before access", async () => {
  let stdout = "";
  let stderr = "";
  let fetchCalls = 0;
  let execCalls = 0;
  const code = await runProductionLiveQaCli({
    argv: ["--compute-claim-recover", "--compute-claim-target-json", JSON.stringify(COMPUTE_CLAIM_TARGET)],
    env: {
      OPL_MERGED_SHA: BASIC_CANARY_MERGED_SHA,
      OPL_COMPUTE_CLAIM_CLOUD_DIGEST: BASIC_CANARY_CLOUD_DIGEST,
      OPL_COMPUTE_CLAIM_RECOVERY_APPROVAL_JSON: computeClaimApprovalJson(),
      OPL_K8S_NAMESPACE: "opl-cloud",
      KUBECONFIG: "/run/secrets/kubeconfig"
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { fetchCalls += 1; return json({}); },
    execFileImpl: async () => { execCalls += 1; return { stdout: "{}" }; }
  });
  assert.equal(code, 1);
  const artifact = JSON.parse(stdout);
  assert.deepEqual(artifact, {
    schemaVersion: 2,
    operationMode: "compute_claim_recover",
    status: "blocked",
    recoveryEligible: false,
    errorCode: "compute_claim_recovery_failed",
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
  });
  assert.doesNotMatch(`${stdout}\n${stderr}`, /approval_required|password|secret|token|provider payload/i);
  assert.equal(fetchCalls, 0);
  assert.equal(execCalls, 0);
});

test("all compute-claim CLI modes emit a non-empty redacted blocked artifact on exceptions", async () => {
  const contract = JSON.parse(await readFile(new URL("../../packages/contracts/opl-cloud-deployment-contract.json", import.meta.url), "utf8"));
  for (const [flag, operationMode, errorCode] of [
    ["--compute-claim-diagnose", "compute_claim_diagnose", "compute_claim_diagnosis_failed"],
    ["--compute-claim-validate", "compute_claim_validate", "compute_claim_validation_failed"],
    ["--compute-claim-recover", "compute_claim_recover", "compute_claim_recovery_failed"],
    ["--compute-claim-continue", "compute_claim_recover_continuation", "compute_claim_continuation_failed"]
  ]) {
    let stdout = "";
    let stderr = "";
    const code = await runProductionLiveQaCli({
      argv: [flag, "--read-only"],
      env: { OPL_SUB2API_ADMIN_PASSWORD: "must-not-leak" },
      stdout: { write: (chunk) => { stdout += chunk; } },
      stderr: { write: (chunk) => { stderr += chunk; } }
    });
    assert.equal(code, 1);
    assert.notEqual(stdout.trim(), "");
    const artifact = JSON.parse(stdout);
    assert.equal(artifact.schemaVersion, 2);
    assert.equal(artifact.operationMode, operationMode);
    assert.equal(artifact.status, "blocked");
    assert.equal(artifact.recoveryEligible, false);
    assert.equal(artifact.errorCode, errorCode);
    assert.deepEqual(artifact.runnerDirectMutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
    assert.deepEqual(
      Object.keys(artifact).sort(),
      [...contract.productionComputeClaimRecovery.artifact.blockedArtifactFieldsByMode[operationMode]].sort()
    );
    if (operationMode === "compute_claim_recover_continuation") {
      assert.equal(artifact.backgroundMutationCountsState, "unknown");
    }
    assert.doesNotMatch(`${stdout}\n${stderr}`, /must-not-leak|password|secret|token|provider payload|conflict/i);
  }
});

function manualReviewDiagnosisFixture({
  storageOperation = null,
  providerTruthAvailable = true,
  providerTruthStorageState = "unknown",
  nodeAvailable = true,
  nodeTaints = [{ key: "oplcloud.cn/workspace-id", value: "unallocated", effect: "NoSchedule" }]
} = {}) {
  const calls = [];
  const execCalls = [];
  const allocation = {
    id: MANUAL_REVIEW_DIAGNOSE_TARGET.computeAllocationId,
    accountId: MANUAL_REVIEW_DIAGNOSE_TARGET.accountId,
    workspaceId: MANUAL_REVIEW_DIAGNOSE_TARGET.workspaceId,
    packageId: "basic",
    nodePoolId: MANUAL_REVIEW_DIAGNOSE_TARGET.nodePoolId,
    machineName: MANUAL_REVIEW_DIAGNOSE_TARGET.machineId,
    nodeName: MANUAL_REVIEW_DIAGNOSE_TARGET.nodeName,
    privateIp: MANUAL_REVIEW_DIAGNOSE_TARGET.nodeName,
    instanceId: MANUAL_REVIEW_DIAGNOSE_TARGET.cvmInstanceId,
    cvmInstanceId: MANUAL_REVIEW_DIAGNOSE_TARGET.cvmInstanceId,
    instanceType: "SA5.MEDIUM4",
    zone: "na-siliconvalley-1",
    chargeType: "PREPAID",
    renewFlag: "NOTIFY_AND_MANUAL_RENEW",
    status: "failed",
    providerData: {
      instanceType: "SA5.MEDIUM4",
      zone: "na-siliconvalley-1",
      chargeType: "PREPAID",
      renewFlag: "NOTIFY_AND_MANUAL_RENEW"
    }
  };
  const ownership = {
    resourceId: MANUAL_REVIEW_DIAGNOSE_TARGET.computeAllocationId,
    accountId: MANUAL_REVIEW_DIAGNOSE_TARGET.accountId,
    workspaceId: MANUAL_REVIEW_DIAGNOSE_TARGET.workspaceId,
    packageId: "basic",
    nodePoolId: MANUAL_REVIEW_DIAGNOSE_TARGET.nodePoolId,
    machineId: MANUAL_REVIEW_DIAGNOSE_TARGET.machineId,
    nodeName: MANUAL_REVIEW_DIAGNOSE_TARGET.nodeName,
    instanceId: MANUAL_REVIEW_DIAGNOSE_TARGET.cvmInstanceId,
    status: "quarantined",
    providerRequestId: "must-not-emit-provider-request-id"
  };
  const computeOperation = {
    id: "fabric-compute-operation",
    operationId: `${MANUAL_REVIEW_DIAGNOSE_TARGET.launchOperationId}:compute`,
    action: "create_compute_allocation",
    resourceId: MANUAL_REVIEW_DIAGNOSE_TARGET.computeAllocationId,
    accountId: MANUAL_REVIEW_DIAGNOSE_TARGET.accountId,
    workspaceId: MANUAL_REVIEW_DIAGNOSE_TARGET.workspaceId,
    status: "failed",
    providerRequestId: "must-not-emit-provider-request-id",
    redactedProviderPayload: {
      allocationPlan: {
        nodePoolId: MANUAL_REVIEW_DIAGNOSE_TARGET.nodePoolId,
        instanceType: "SA5.MEDIUM4"
      }
    }
  };
  const truth = {
    computeState: "ready",
    storageState: providerTruthStorageState,
    compute: {
      ...allocation,
      providerResourceId: MANUAL_REVIEW_DIAGNOSE_TARGET.cvmInstanceId
    },
    storage: { id: MANUAL_REVIEW_DIAGNOSE_TARGET.storageId }
  };
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    const headers = new Headers(init.headers);
    calls.push({ method, path: url.pathname, search: url.search, headers });
    assert.equal(url.hostname, "fabric.opl-cloud.svc");
    assert.equal(headers.get("authorization"), "Bearer internal-service-token");
    if (url.pathname === `/fabric/compute-allocations/${MANUAL_REVIEW_DIAGNOSE_TARGET.computeAllocationId}`) return json(allocation);
    if (url.pathname === `/fabric/machine-ownerships/${MANUAL_REVIEW_DIAGNOSE_TARGET.computeAllocationId}`) return json(ownership);
    if (url.pathname === "/fabric/operations") return json([computeOperation, ...(storageOperation ? [storageOperation] : [])]);
    if (url.pathname === "/fabric/monthly-provider-truth") {
      if (!providerTruthAvailable) return json({ error: "monthly_provider_truth_unavailable" }, 503);
      return json(truth);
    }
    return json({ error: "not_found" }, 404);
  };
  const execFileImpl = async (command, args) => {
    execCalls.push({ command, args });
    assert.equal(command, "kubectl");
    if (!nodeAvailable) throw new Error("node_get_unavailable");
    return {
      stdout: JSON.stringify({
        metadata: {
          name: MANUAL_REVIEW_DIAGNOSE_TARGET.nodeName,
          labels: {
            "oplcloud.cn/resource-id": MANUAL_REVIEW_DIAGNOSE_TARGET.computeAllocationId,
            "oplcloud.cn/account-id": MANUAL_REVIEW_DIAGNOSE_TARGET.accountId,
            "oplcloud.cn/workspace-id": MANUAL_REVIEW_DIAGNOSE_TARGET.workspaceId
          }
        },
        spec: {
          taints: nodeTaints
        },
        status: {
          addresses: [{ type: "InternalIP", address: MANUAL_REVIEW_DIAGNOSE_TARGET.nodeName }]
        }
      })
    };
  };
  return { calls, execCalls, fetchImpl, execFileImpl };
}

test("manual-review diagnose emits a redacted read-only identity artifact for an unstarted storage volume", async () => {
  assert.equal(typeof productionLiveQa.diagnoseManualReviewRecovery, "function");
  const fixture = manualReviewDiagnosisFixture();
  const result = await productionLiveQa.diagnoseManualReviewRecovery({
    fabricOrigin: "http://fabric.opl-cloud.svc:8082",
    internalServiceToken: "internal-service-token",
    target: MANUAL_REVIEW_DIAGNOSE_TARGET,
    kubeconfigPath: "/run/secrets/kubeconfig",
    execFileImpl: fixture.execFileImpl,
    fetchImpl: fixture.fetchImpl
  });

  assert.deepEqual(Object.keys(result).sort(), [
    "allocation", "computeOperation", "errorCode", "identity", "mutationCounts", "node", "operationMode", "ownership", "providerTruth", "recoveryEligible", "schemaVersion", "status", "storage"
  ]);
  assert.deepEqual(result, {
    schemaVersion: 1,
    operationMode: "manual_review_diagnose",
    status: "diagnosed",
    recoveryEligible: true,
    errorCode: "none",
    allocation: { state: "present", status: "failed" },
    ownership: { state: "present", status: "quarantined" },
    computeOperation: { state: "present", status: "failed" },
    providerTruth: { state: "available", computeState: "ready", storageState: "unknown", errorCode: "none" },
    identity: {
      accountMatches: true,
      workspaceMatches: true,
      launchOperationMatches: true,
      poolMatches: true,
      machineMatches: true,
      cvmMatches: true,
      nodeMatches: true,
      privateIpMatches: true,
      skuMatches: true,
      zoneMatches: true,
      prepaidMatches: true,
      manualRenewMatches: true
    },
    node: {
      resourceIdLabelMatches: true,
      accountIdLabelMatches: true,
      workspaceIdLabelMatches: true,
      unallocatedTaint: true
    },
    storage: { state: "not_started" },
    mutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 }
  });
  assert.equal(fixture.calls.length, 4);
  assert.equal(fixture.calls.every((call) => call.method === "GET"), true);
  assert.deepEqual(fixture.execCalls, [{
    command: "kubectl",
    args: ["--kubeconfig", "/run/secrets/kubeconfig", "get", "node", MANUAL_REVIEW_DIAGNOSE_TARGET.nodeName, "-o", "json"]
  }]);
  assert.doesNotMatch(JSON.stringify(result), /providerRequestId|must-not-emit|token|secret|password|raw/i);
});

test("manual-review diagnose uses the Ready Fabric Pod without passing a runner token", async () => {
  const fixture = manualReviewDiagnosisFixture();
  const podExecCalls = [];
  const execFileImpl = async (command, args, options) => {
    podExecCalls.push({ command, args });
    if (!args.includes("exec")) return fixture.execFileImpl(command, args, options);
    const path = String(args.at(-1));
    const response = await fixture.fetchImpl(`http://fabric.opl-cloud.svc:8082${path}`, {
      method: "GET",
      headers: { authorization: "Bearer internal-service-token" }
    });
    const body = await response.json();
    return {
      stdout: JSON.stringify({
        statusCode: response.status,
        payload: response.status === 200 ? body : null,
        errorCode: response.status === 200 ? "none" : body.error
      })
    };
  };

  const result = await productionLiveQa.diagnoseManualReviewRecovery({
    target: MANUAL_REVIEW_DIAGNOSE_TARGET,
    kubeconfigPath: "/run/secrets/kubeconfig",
    fabricPod: "opl-cloud-fabric-7b8c9d",
    fabricNamespace: "opl-cloud",
    execFileImpl
  });

  assert.equal(result.recoveryEligible, true);
  assert.equal(fixture.calls.length, 4);
  const fabricReads = podExecCalls.filter(({ args }) => args.includes("exec"));
  assert.equal(fabricReads.length, 4);
  assert.equal(fabricReads.every(({ command, args }) =>
    command === "kubectl" && args.includes("exec") && args.includes("opl-cloud-fabric-7b8c9d") && args.includes("node") &&
    !args.some((value) => String(value).includes("internal-service-token"))), true);
  assert.equal(podExecCalls.some(({ args }) => args.includes("port-forward")), false);
});

test("manual-review diagnose preserves a Fabric authorization failure as a safe error code", async () => {
  const fixture = manualReviewDiagnosisFixture();
  const execFileImpl = async (command, args, options) => {
    if (args.includes("exec")) {
      return { stdout: JSON.stringify({ statusCode: 401, payload: null, errorCode: "unauthorized" }) };
    }
    return fixture.execFileImpl(command, args, options);
  };
  const result = await productionLiveQa.diagnoseManualReviewRecovery({
    target: MANUAL_REVIEW_DIAGNOSE_TARGET,
    kubeconfigPath: "/run/secrets/kubeconfig",
    fabricPod: "opl-cloud-fabric-7b8c9d",
    fabricNamespace: "opl-cloud",
    execFileImpl
  });

  assert.equal(result.recoveryEligible, false);
  assert.equal(result.errorCode, "fabric_get_unauthorized");
  assert.deepEqual(result.mutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
});

test("manual-review diagnose blocks an unknown storage create attempt without any write", async () => {
  const fixture = manualReviewDiagnosisFixture({
    storageOperation: {
      id: "fabric-storage-operation",
      operationId: `${MANUAL_REVIEW_DIAGNOSE_TARGET.launchOperationId}:storage`,
      action: "create_storage_volume",
      resourceId: MANUAL_REVIEW_DIAGNOSE_TARGET.storageId,
      accountId: MANUAL_REVIEW_DIAGNOSE_TARGET.accountId,
      workspaceId: MANUAL_REVIEW_DIAGNOSE_TARGET.workspaceId,
      status: "failed",
      providerRequestId: "must-not-emit-provider-request-id"
    }
  });
  const result = await productionLiveQa.diagnoseManualReviewRecovery({
    fabricOrigin: "http://fabric.opl-cloud.svc:8082",
    internalServiceToken: "internal-service-token",
    target: MANUAL_REVIEW_DIAGNOSE_TARGET,
    kubeconfigPath: "/run/secrets/kubeconfig",
    execFileImpl: fixture.execFileImpl,
    fetchImpl: fixture.fetchImpl
  });

  assert.equal(result.recoveryEligible, false);
  assert.equal(result.errorCode, "storage_create_attempt_unknown");
  assert.deepEqual(result.storage, { state: "attempted_unknown" });
  assert.equal(fixture.calls.every((call) => call.method === "GET"), true);
  assert.equal(fixture.execCalls.every(({ args }) => args.includes("get") && !args.some((value) => ["apply", "delete", "label", "taint", "exec", "patch"].includes(value))), true);
});

test("manual-review diagnose fails closed on unavailable provider truth without any mutation", async () => {
  const fixture = manualReviewDiagnosisFixture({ providerTruthAvailable: false });
  const result = await productionLiveQa.diagnoseManualReviewRecovery({
    fabricOrigin: "http://fabric.opl-cloud.svc:8082",
    internalServiceToken: "internal-service-token",
    target: MANUAL_REVIEW_DIAGNOSE_TARGET,
    kubeconfigPath: "/run/secrets/kubeconfig",
    execFileImpl: fixture.execFileImpl,
    fetchImpl: fixture.fetchImpl
  });

  assert.equal(result.recoveryEligible, false);
  assert.equal(result.errorCode, "monthly_provider_truth_unavailable");
  assert.deepEqual(result.providerTruth, {
    state: "unavailable",
    computeState: "unknown",
    storageState: "unknown",
    errorCode: "monthly_provider_truth_unavailable"
  });
  assert.deepEqual(result.mutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
  assert.equal(fixture.calls.every((call) => call.method === "GET"), true);
});

test("manual-review diagnose reports a node read failure before identity mismatch", async () => {
  const fixture = manualReviewDiagnosisFixture({ nodeAvailable: false });
  const result = await productionLiveQa.diagnoseManualReviewRecovery({
    fabricOrigin: "http://fabric.opl-cloud.svc:8082",
    internalServiceToken: "internal-service-token",
    target: MANUAL_REVIEW_DIAGNOSE_TARGET,
    kubeconfigPath: "/run/secrets/kubeconfig",
    execFileImpl: fixture.execFileImpl,
    fetchImpl: fixture.fetchImpl
  });

  assert.equal(result.recoveryEligible, false);
  assert.equal(result.errorCode, "node_get_unavailable");
  assert.deepEqual(result.mutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
  assert.equal(fixture.calls.every((call) => call.method === "GET"), true);
});

test("manual-review diagnose blocks storage truth without a matching storage operation", async () => {
  const fixture = manualReviewDiagnosisFixture({ providerTruthStorageState: "ready" });
  const result = await productionLiveQa.diagnoseManualReviewRecovery({
    fabricOrigin: "http://fabric.opl-cloud.svc:8082",
    internalServiceToken: "internal-service-token",
    target: MANUAL_REVIEW_DIAGNOSE_TARGET,
    kubeconfigPath: "/run/secrets/kubeconfig",
    execFileImpl: fixture.execFileImpl,
    fetchImpl: fixture.fetchImpl
  });

  assert.equal(result.recoveryEligible, false);
  assert.equal(result.errorCode, "storage_create_attempt_unknown");
  assert.deepEqual(result.storage, { state: "attempted_unknown" });
  assert.deepEqual(result.mutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
});

test("manual-review diagnose rejects an ambiguous workspace ownership taint", async () => {
  const fixture = manualReviewDiagnosisFixture({
    nodeTaints: [
      { key: "oplcloud.cn/workspace-id", value: "unallocated", effect: "NoSchedule" },
      { key: "oplcloud.cn/workspace-id", value: MANUAL_REVIEW_DIAGNOSE_TARGET.workspaceId, effect: "NoSchedule" }
    ]
  });
  const result = await productionLiveQa.diagnoseManualReviewRecovery({
    fabricOrigin: "http://fabric.opl-cloud.svc:8082",
    internalServiceToken: "internal-service-token",
    target: MANUAL_REVIEW_DIAGNOSE_TARGET,
    kubeconfigPath: "/run/secrets/kubeconfig",
    execFileImpl: fixture.execFileImpl,
    fetchImpl: fixture.fetchImpl
  });

  assert.equal(result.recoveryEligible, false);
  assert.equal(result.errorCode, "node_ownership_or_taint_mismatch");
  assert.equal(result.node.unallocatedTaint, false);
  assert.deepEqual(result.mutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
});

test("manual-review diagnose implementation uses only Fabric GET and no Kubernetes write commands", async () => {
  const source = await readFile(new URL("../../tools/production-live-qa.ts", import.meta.url), "utf8");
  const helperStart = source.indexOf("const MANUAL_REVIEW_FABRIC_POD_GET_SCRIPT");
  const start = source.indexOf("export async function diagnoseManualReviewRecovery");
  const end = source.indexOf("\nconst COMPUTE_CLAIM_DIAGNOSE_MODE", start);
  assert.notEqual(helperStart, -1);
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const readOnlyImplementation = source.slice(helperStart, end);
  assert.match(readOnlyImplementation, /method: "GET"/);
  assert.match(readOnlyImplementation, /fabricPod/);
  assert.match(readOnlyImplementation, /"exec"/);
  assert.match(readOnlyImplementation, /http\.get/);
  assert.match(readOnlyImplementation, /"kubectl"[\s\S]*"get", "node"/);
  assert.doesNotMatch(readOnlyImplementation, /method:\s*"(?:POST|PUT|PATCH|DELETE)"/);
  assert.doesNotMatch(readOnlyImplementation, /"(?:apply|delete|label|taint|patch)"/);
  assert.doesNotMatch(readOnlyImplementation, /wallet-adjustments|workspace-launches|\/recover/);

	const cliStart = source.indexOf('if (args["manual-review-diagnose"] === "true")');
	const cliEnd = source.indexOf('if (args["workspace-launch-readback-diagnose"] === "true")', cliStart);
  assert.notEqual(cliStart, -1);
  assert.notEqual(cliEnd, -1);
  const cliDiagnose = source.slice(cliStart, cliEnd);
  assert.match(cliDiagnose, /fabricPod: args\["fabric-pod"\]/);
  assert.match(cliDiagnose, /fabricNamespace: args\["fabric-namespace"\]/);
  assert.doesNotMatch(cliDiagnose, /internalServiceToken|OPL_INTERNAL_SERVICE_TOKEN/);
});

test("customer Basic canary uses one launch POST and returns redacted end-to-end evidence", async () => {
  const fixture = basicCanaryFixture();
  const result = await productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture));

  assert.equal(result.ok, true);
  assert.equal(result.status, "passed");
  assert.equal(result.accountId, BASIC_CANARY_ACCOUNT_ID);
  assert.equal(result.workspaceId, BASIC_CANARY_WORKSPACE_ID);
  assert.equal(result.operationId, BASIC_CANARY_LAUNCH_OPERATION_ID);
  assert.deepEqual(result.wallet, {
    operationId: BASIC_CANARY_WALLET_OPERATION_ID,
    source: "wallet_adjustment_authoritative_readback",
    beforeUsdMicros: "0",
    afterUsdMicros: "100000000",
    deltaUsdMicros: "100000000"
  });
  assert.deepEqual(result.compute.procurement, { nodePoolId: "np-basic", baselineReplicas: 7, targetReplicas: 8, beforeMachineCount: 7, machineWasNew: true });
  assert.equal(result.compute.instanceId, "ins-basic-canary");
  assert.equal(result.compute.sku, BASIC_CANARY_RESOLVED_INSTANCE_TYPE);
  assert.deepEqual(result.compute.resources, { cpu: 2, memoryGb: 4 });
  assert.equal(result.storage.providerId, "disk-basic-canary");
  assert.equal(result.receipt.type, "billing.workspace_purchased.v1");
  assert.equal(result.receipt.totalUsdMicros, 52_580_000);
  assert.equal(result.usage.requestId, "req-basic-canary");
  assert.equal(result.usage.actualCostUsdMicros, 120);
  assert.equal(result.runtime.pod.imageID, `containerd://${BASIC_CANARY_DIGEST}`);
  assert.equal(result.runtime.pod.nodeName, "10.66.1.18");
  assert.equal(result.runtime.pod.nodeName, result.compute.nodeName);
  assert.deepEqual(result.runtime.pod.resources, { cpu: 2, memoryGb: 4 });
  assert.equal(result.runtime.id, "runtime-basic-canary");
  assert.equal(result.runtime.providerId, "workspace-service-basic-canary");
  assert.equal(result.runtime.websocket.status, 101);
  assert.deepEqual(result.writeCounts, {
    accountProvisionPosts: 1,
    walletAdjustmentPosts: 1,
    workspaceLaunchPosts: 1,
    modelRequests: 1,
    workspaceKeysCreated: 1,
    workspacePurchaseDebits: 1,
    tencentCvmPurchases: 1,
    tencentCbsPurchases: 1
  });
  assert.deepEqual(result.httpAttempts, {
    accountProvision: null,
    walletAdjustment: null,
    workspaceLaunch: null,
    modelRequest: null
  });
  assert.equal(fixture.state.provisionPosts, 1);
  assert.equal(fixture.state.rechargePosts, 1);
  assert.equal(fixture.state.launchPosts, 1);
  assert.equal(fixture.state.launchPolls, 2);
  assert.equal(fixture.state.modelRequests, 1);
  assert.equal(fixture.state.podReads, 1);
  assert.equal(fixture.state.cloudRevisionReads, 5);
  assert.equal(fixture.calls.filter((call) => call.host === "fabric.opl-cloud.svc:8082" && call.path === "/fabric/catalog" && call.method === "GET").length, 1);
  assert.equal(fixture.calls.filter((call) => call.path === "/api/workspace-launches" && call.method === "POST").length, 1);
  assert.equal(fixture.calls.filter((call) => call.path === `/api/workspace-launches/${BASIC_CANARY_LAUNCH_OPERATION_ID}`).every((call) => call.method === "GET"), true);
  assert.doesNotMatch(JSON.stringify(result), /customer-password|workspace-password|internal-service-token|must-not-emit|redeem/i);
});

test("customer Basic canary ignores the wallet POST payload and validates the immediate authoritative GET", async () => {
  const fixture = basicCanaryFixture({
    walletAdjustmentPostPayload: { status: "succeeded", phase: "complete", operationId: "wrong-post-operation" }
  });

  const result = await productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture));

  assert.equal(result.status, "passed");
  assert.equal(fixture.state.rechargePosts, 1);
  const walletPostIndex = fixture.calls.findIndex((call) =>
    call.path === `/api/operator/accounts/${BASIC_CANARY_ACCOUNT_ID}/wallet-adjustments` && call.method === "POST");
  const walletGetIndexes = fixture.calls.flatMap((call, index) =>
    call.path === `/api/operator/wallet-adjustments/${BASIC_CANARY_WALLET_OPERATION_ID}` && call.method === "GET" ? [index] : []);
  assert.equal(walletGetIndexes.length, 2);
  assert.equal(walletGetIndexes[1] > walletPostIndex, true);
});

test("customer Basic canary rejects a wallet authoritative GET with phase succeeded", async () => {
  const fixture = basicCanaryFixture({
    initialProvisioned: true,
    initialRecharged: true,
    walletAdjustmentPhase: "succeeded"
  });

  await assert.rejects(
    () => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)),
    /production_basic_canary_recharge_readback_failed/
  );
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 0);
});

test("customer Basic canary recovers an existing 60000000-micros wallet operation with zero recharge POSTs", async () => {
  const fixture = basicCanaryFixture({
    initialProvisioned: true,
    initialRecharged: true,
    rechargeUsdMicros: 60_000_000
  });

  const result = await productionLiveQa.verifyProductionBasicCustomerCanary(
    basicCanaryOptions(fixture, { rechargeUsdMicros: "60000000" })
  );

  assert.equal(result.status, "passed");
  assert.equal(result.wallet.deltaUsdMicros, "60000000");
  assert.equal(fixture.state.provisionPosts, 0);
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 1);
  assert.equal(result.writeCounts.accountProvisionPosts, 0);
  assert.equal(result.writeCounts.walletAdjustmentPosts, 0);
  assert.equal(result.writeCounts.workspaceLaunchPosts, 1);
});

test("customer Basic canary uses the current live wallet rather than the immutable recharge after-balance", async () => {
  const existingGeneralKeys = [1, 2, 3, 4].map((id) => generalKey(id));
  const fixture = basicCanaryFixture({
    initialProvisioned: true,
    initialRecharged: true,
    rechargeUsdMicros: 60_000_000,
    existingGeneralKeys,
    generalKeySpendBeforeLaunchUsdMicros: 1,
    generalKeySpendAfterModelUsdMicros: 1
  });

  const result = await productionLiveQa.verifyProductionBasicCustomerCanary(
    basicCanaryOptions(fixture, { rechargeUsdMicros: "60000000" })
  );

  assert.equal(result.status, "passed");
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 1);
  assert.equal(fixture.state.modelRequests, 1);
  assert.deepEqual(result.keyEvidence.generalKeyIds, ["1", "2", "3", "4"]);
  assert.deepEqual(result.wallet, {
    operationId: BASIC_CANARY_WALLET_OPERATION_ID,
    source: "wallet_adjustment_authoritative_readback",
    beforeUsdMicros: "0",
    afterUsdMicros: "60000000",
    deltaUsdMicros: "60000000"
  });
});

test("customer Basic canary recovers the exact historical precharge without its raw wallet idempotency key", async () => {
  const existingGeneralKeys = [1, 2, 3, 4].map((id) => generalKey(id));
  const fixture = basicCanaryFixture({
    initialProvisioned: true,
    initialRecharged: true,
    rechargeUsdMicros: 60_000_000,
    existingGeneralKeys
  });

  const result = await productionLiveQa.verifyProductionBasicCustomerCanary(
    recoveredPrechargeBasicCanaryOptions(fixture)
  );

  assert.equal(result.status, "passed");
  assert.deepEqual(result.wallet, {
    operationId: BASIC_CANARY_WALLET_OPERATION_ID,
    source: "wallet_adjustment_authoritative_readback",
    beforeUsdMicros: "0",
    afterUsdMicros: "60000000",
    deltaUsdMicros: "60000000"
  });
  assert.deepEqual(result.keyEvidence.generalKeyIds, ["1", "2", "3", "4"]);
  assert.deepEqual(result.writeCounts, {
    accountProvisionPosts: 0,
    walletAdjustmentPosts: 0,
    workspaceLaunchPosts: 1,
    modelRequests: 1,
    workspaceKeysCreated: 1,
    workspacePurchaseDebits: 1,
    tencentCvmPurchases: 1,
    tencentCbsPurchases: 1
  });
  assert.deepEqual(result.httpAttempts, { workspaceLaunch: null, modelRequest: null });
  assert.equal(fixture.state.provisionPosts, 0);
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 1);
  assert.equal(fixture.state.workspacePurchaseDebits, 1);
  assert.equal(fixture.state.tencentCvmPurchases, 1);
  assert.equal(fixture.state.tencentCbsPurchases, 1);
  assert.equal(fixture.state.modelRequests, 1);
  assert.equal(fixture.calls.filter((call) => call.path === "/api/operator/accounts" && call.method === "POST").length, 0);
  assert.deepEqual(
    fixture.calls.filter((call) => call.path.includes("wallet-adjustments")).map((call) => `${call.method} ${call.path}`),
    [`GET /api/operator/wallet-adjustments/${BASIC_CANARY_WALLET_OPERATION_ID}`]
  );
});

test("customer Basic canary rejects an explicit empty funding mode before any network call", async () => {
  const fixture = basicCanaryFixture();
  const approval = JSON.parse(basicCanaryApprovalJson());
  approval.fundingMode = "";

  await assert.rejects(
    () => productionLiveQa.verifyProductionBasicCustomerCanary({
      ...basicCanaryOptions(fixture),
      approvalJson: JSON.stringify(approval)
    }),
    /production_basic_canary_approval_invalid/
  );
  assert.equal(fixture.calls.length, 0);
  assert.deepEqual({
    account: fixture.state.provisionPosts,
    wallet: fixture.state.rechargePosts,
    launch: fixture.state.launchPosts,
    debit: fixture.state.workspacePurchaseDebits,
    cvm: fixture.state.tencentCvmPurchases,
    cbs: fixture.state.tencentCbsPurchases,
    model: fixture.state.modelRequests
  }, { account: 0, wallet: 0, launch: 0, debit: 0, cvm: 0, cbs: 0, model: 0 });
});

for (const [name, approval] of [
  ["launch operation", { launchOperationId: "workspace-launch-wrong" }],
  ["workspace", { workspaceId: "ws-wrong" }]
]) {
  test(`customer Basic canary recovery rejects an approval with a mismatched expected ${name} before writes`, async () => {
    const fixture = basicCanaryFixture({
      initialProvisioned: true,
      initialRecharged: true,
      rechargeUsdMicros: 60_000_000
    });

    await assert.rejects(
      () => productionLiveQa.verifyProductionBasicCustomerCanary(recoveredPrechargeBasicCanaryOptions(fixture, approval)),
      /production_basic_canary_approval_invalid/
    );
    assert.deepEqual({
      account: fixture.state.provisionPosts,
      wallet: fixture.state.rechargePosts,
      launch: fixture.state.launchPosts,
      debit: fixture.state.workspacePurchaseDebits,
      cvm: fixture.state.tencentCvmPurchases,
      cbs: fixture.state.tencentCbsPurchases,
      model: fixture.state.modelRequests
    }, { account: 0, wallet: 0, launch: 0, debit: 0, cvm: 0, cbs: 0, model: 0 });
  });
}

for (const [name, initialWalletUsdMicros] of [
  ["equals", 52_580_000],
  ["is below", 52_579_999]
]) {
  test(`customer Basic canary recovery blocks launch when live wallet ${name} the quote`, async () => {
    const fixture = basicCanaryFixture({
      initialProvisioned: true,
      initialRecharged: true,
      rechargeUsdMicros: 60_000_000,
      initialWalletUsdMicros
    });

    await assert.rejects(
      () => productionLiveQa.verifyProductionBasicCustomerCanary(recoveredPrechargeBasicCanaryOptions(fixture)),
      /production_basic_canary_live_wallet_insufficient/
    );
    assert.deepEqual({
      account: fixture.state.provisionPosts,
      wallet: fixture.state.rechargePosts,
      launch: fixture.state.launchPosts,
      debit: fixture.state.workspacePurchaseDebits,
      cvm: fixture.state.tencentCvmPurchases,
      cbs: fixture.state.tencentCbsPurchases,
      model: fixture.state.modelRequests
    }, { account: 0, wallet: 0, launch: 0, debit: 0, cvm: 0, cbs: 0, model: 0 });
    assert.deepEqual(
      fixture.calls.filter((call) => call.path.includes("wallet-adjustments")).map((call) => `${call.method} ${call.path}`),
      [`GET /api/operator/wallet-adjustments/${BASIC_CANARY_WALLET_OPERATION_ID}`]
    );
  });
}

for (const [name, walletAdjustmentOverrides] of [
  ["operation", { operationId: "wallet-adjustment-wrong" }],
  ["account", { accountId: "acct-wrong" }],
  ["amount", { amountUsd: "59.000000" }],
  ["reason", { reason: "wrong reason" }],
  ["status", { status: "pending" }],
  ["phase", { phase: "succeeded" }],
  ["delta", {
    beforeBalance: nestedSource({ currency: "USD", usdMicros: "1" }, "sub2api"),
    afterBalance: nestedSource({ currency: "USD", usdMicros: "60000000" }, "sub2api")
  }]
]) {
  test(`customer Basic canary recovery fails closed when the historical precharge ${name} is invalid`, async () => {
    const fixture = basicCanaryFixture({
      initialProvisioned: true,
      initialRecharged: true,
      rechargeUsdMicros: 60_000_000,
      walletAdjustmentOverrides
    });

    await assert.rejects(
      () => productionLiveQa.verifyProductionBasicCustomerCanary(recoveredPrechargeBasicCanaryOptions(fixture)),
      /production_basic_canary_recharge_readback_failed/
    );
    assert.deepEqual({
      account: fixture.state.provisionPosts,
      wallet: fixture.state.rechargePosts,
      launch: fixture.state.launchPosts,
      debit: fixture.state.workspacePurchaseDebits,
      cvm: fixture.state.tencentCvmPurchases,
      cbs: fixture.state.tencentCbsPurchases,
      model: fixture.state.modelRequests
    }, { account: 0, wallet: 0, launch: 0, debit: 0, cvm: 0, cbs: 0, model: 0 });
    assert.equal(fixture.calls.filter((call) => call.path.includes("wallet-adjustments") && call.method !== "GET").length, 0);
  });
}

test("customer Basic canary recovery requires the approved existing account without provisioning it", async () => {
  const fixture = basicCanaryFixture({ initialRecharged: true, rechargeUsdMicros: 60_000_000 });

  await assert.rejects(
    () => productionLiveQa.verifyProductionBasicCustomerCanary(recoveredPrechargeBasicCanaryOptions(fixture)),
    /production_basic_canary_existing_account_required/
  );
  assert.equal(fixture.state.provisionPosts, 0);
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 0);
  assert.equal(fixture.calls.filter((call) => call.path.includes("wallet-adjustments")).length, 0);
});

test("customer Basic canary recovery fails closed when the approved historical precharge is absent", async () => {
  const fixture = basicCanaryFixture({ initialProvisioned: true });

  await assert.rejects(
    () => productionLiveQa.verifyProductionBasicCustomerCanary(recoveredPrechargeBasicCanaryOptions(fixture)),
    /production_basic_canary_recharge_readback_failed/
  );
  assert.deepEqual({
    account: fixture.state.provisionPosts,
    wallet: fixture.state.rechargePosts,
    launch: fixture.state.launchPosts,
    debit: fixture.state.workspacePurchaseDebits,
    cvm: fixture.state.tencentCvmPurchases,
    cbs: fixture.state.tencentCbsPurchases,
    model: fixture.state.modelRequests
  }, { account: 0, wallet: 0, launch: 0, debit: 0, cvm: 0, cbs: 0, model: 0 });
  assert.deepEqual(
    fixture.calls.filter((call) => call.path.includes("wallet-adjustments")).map((call) => `${call.method} ${call.path}`),
    [`GET /api/operator/wallet-adjustments/${BASIC_CANARY_WALLET_OPERATION_ID}`]
  );
});

test("customer Basic canary recovery rejects a pre-existing Workspace Key before launch", async () => {
  const fixture = basicCanaryFixture({
    initialProvisioned: true,
    initialRecharged: true,
    rechargeUsdMicros: 60_000_000,
    existingWorkspaceKeys: [workspaceKey("77")]
  });

  await assert.rejects(
    () => productionLiveQa.verifyProductionBasicCustomerCanary(recoveredPrechargeBasicCanaryOptions(fixture)),
    /production_basic_canary_baseline_not_empty/
  );
  assert.equal(fixture.state.provisionPosts, 0);
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 0);
  assert.equal(fixture.calls.filter((call) => call.path.includes("wallet-adjustments") && call.method !== "GET").length, 0);
});

for (const [name, generalKeySpendBeforeLaunchUsdMicros] of [
  ["equals", 7_420_000],
  ["is below", 7_420_001]
]) {
  test(`customer Basic canary blocks launch when the current live wallet ${name} the quote`, async () => {
    const fixture = basicCanaryFixture({
      initialProvisioned: true,
      initialRecharged: true,
      rechargeUsdMicros: 60_000_000,
      generalKeySpendBeforeLaunchUsdMicros
    });

    await assert.rejects(
      () => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture, { rechargeUsdMicros: "60000000" })),
      /production_basic_canary_live_wallet_insufficient/
    );
    assert.equal(fixture.state.rechargePosts, 0);
    assert.equal(fixture.state.launchPosts, 0);
    assert.equal(fixture.state.modelRequests, 0);
  });
}

for (const [name, walletAdjustmentOverrides] of [
  ["operation id", { operationId: "wallet-adjustment-other" }],
  ["account identity", { accountId: "acct-other" }],
  ["kind", { kind: "debit" }],
  ["status", { status: "pending" }],
  ["phase", { phase: "succeeded" }],
  ["amount", { amountUsd: "59.999999" }],
  ["exact recharge delta", { afterBalance: nestedSource({ currency: "USD", usdMicros: "59999999" }, "sub2api") }]
]) {
  test(`customer Basic canary fails closed for an invalid immutable recharge ${name}`, async () => {
    const fixture = basicCanaryFixture({
      initialProvisioned: true,
      initialRecharged: true,
      rechargeUsdMicros: 60_000_000,
      walletAdjustmentOverrides
    });

    await assert.rejects(
      () => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture, { rechargeUsdMicros: "60000000" })),
      /production_basic_canary_recharge_readback_failed/
    );
    assert.equal(fixture.state.rechargePosts, 0);
    assert.equal(fixture.state.launchPosts, 0);
    assert.equal(fixture.state.modelRequests, 0);
  });
}

test("customer Basic canary accepts the real pricing preview DTO without top-level sizeGb or autoRenew", async () => {
  const fixture = basicCanaryFixture();
  const result = await productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture));

  assert.equal(result.status, "passed");
  assert.equal(fixture.state.rechargePosts, 1);
  assert.equal(fixture.state.launchPosts, 1);
});

test("customer Basic canary carries a non-fixed pricing preview amount through launch and receipt", async () => {
  const quoteTotalUsdMicros = 51_234_567;
  const fixture = basicCanaryFixture({
    quoteTotalUsdMicros,
    quoteComputeUsdMicros: 49_000_000,
    quoteStorageUsdMicros: 2_234_567
  });

  const result = await productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture));

  assert.equal(result.status, "passed");
  assert.equal(result.wallet.deltaUsdMicros, "100000000");
  assert.equal(result.receipt.totalUsdMicros, quoteTotalUsdMicros);
  assert.equal(result.receipt.components.compute.chargeUsdMicros + result.receipt.components.storage.chargeUsdMicros, quoteTotalUsdMicros);
  assert.equal(fixture.state.rechargePosts, 1);
  assert.equal(fixture.state.launchPosts, 1);
});

test("customer Basic canary rejects a non-10GiB pricing preview before recharge", async () => {
  const fixture = basicCanaryFixture({ initialProvisioned: true, quoteStorageSizeGb: 20 });

  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_quote_invalid/);
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 0);
  assert.equal(fixture.state.modelRequests, 0);
});

test("customer Basic canary rejects a non-safe pricing preview amount before recharge", async () => {
  const fixture = basicCanaryFixture({ initialProvisioned: true, quoteTotalUsdMicros: Number.MAX_SAFE_INTEGER + 1 });

  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_quote_invalid/);
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 0);
});

test("customer Basic canary rejects an approved recharge that cannot cover the server quote before recharge", async () => {
  const fixture = basicCanaryFixture({
    initialProvisioned: true,
    quoteTotalUsdMicros: 100_000_001,
    quoteComputeUsdMicros: 97_000_000,
    quoteStorageUsdMicros: 3_000_001
  });

  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_recharge_insufficient/);
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 0);
});

for (const [name, override, error] of [
  ["launch", { launchTotalUsdMicros: 51_234_568 }, /production_basic_canary_launch_readback_failed/],
  ["receipt total", { receiptTotalUsdMicros: 51_234_568 }, /production_basic_canary_receipt_invalid/],
  ["receipt components", { receiptComputeUsdMicros: 49_000_001 }, /production_basic_canary_receipt_invalid/]
]) {
  test(`customer Basic canary rejects a ${name} amount that differs from the server quote`, async () => {
    const fixture = basicCanaryFixture({
      quoteTotalUsdMicros: 51_234_567,
      quoteComputeUsdMicros: 49_000_000,
      quoteStorageUsdMicros: 2_234_567,
      ...override
    });

    await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), error);
    assert.equal(fixture.state.rechargePosts, 1);
    assert.equal(fixture.state.launchPosts, 1);
    assert.equal(fixture.state.modelRequests, 0);
  });
}

test("customer Basic canary accepts four existing general Keys and proves only one Workspace Key was added", async () => {
  const existingGeneralKeys = [1, 2, 3, 4].map((id) => generalKey(id, `general-${id}`));
  const fixture = basicCanaryFixture({ existingGeneralKeys });

  const result = await productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture));

  assert.equal(result.status, "passed");
  assert.equal(result.keyEvidence.generalKeysUnchanged, true);
  assert.deepEqual(result.keyEvidence.generalKeyIds, ["1", "2", "3", "4"]);
  assert.deepEqual(result.keyEvidence.workspaceKey, workspaceKey());
  assert.equal(result.writeCounts.workspaceKeysCreated, 1);
  assert.equal(fixture.state.rechargePosts, 1);
  assert.equal(fixture.state.launchPosts, 1);
  assert.equal(fixture.state.modelRequests, 1);
});

test("customer Basic canary fails before recharge when an existing Workspace Key is present", async () => {
  const fixture = basicCanaryFixture({
    initialProvisioned: true,
    existingWorkspaceKeys: [workspaceKey("77")]
  });

  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_baseline_not_empty/);
  assert.equal(fixture.state.provisionPosts, 0);
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 0);
  assert.equal(fixture.state.modelRequests, 0);
});

for (const [name, workspaceKeysAfter] of [
  ["missing target", []],
  ["wrong id", [workspaceKey("92")]],
  ["wrong status", [workspaceKey(BASIC_CANARY_KEY_ID, { status: "disabled" })]],
  ["multiple Workspace Keys", [workspaceKey(), workspaceKey("92", { name: `opl-workspace-${stableCanaryId("second").slice(0, 12)}` })]]
]) {
  test(`customer Basic canary fails closed after launch for ${name}`, async () => {
    const fixture = basicCanaryFixture({
      existingGeneralKeys: [generalKey("1")],
      workspaceKeysAfter
    });

    await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_workspace_or_key_invalid/);
    assert.equal(fixture.state.rechargePosts, 1);
    assert.equal(fixture.state.launchPosts, 1);
    assert.equal(fixture.state.modelRequests, 0);
  });
}

test("customer Basic canary reads a Workspace Key on the second Key page", async () => {
  const existingGeneralKeys = Array.from({ length: 20 }, (_, index) => generalKey(index + 1, `general-${index + 1}`));
  const fixture = basicCanaryFixture({ existingGeneralKeys });

  const result = await productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture));

  assert.equal(result.status, "passed");
  assert.equal(result.keyEvidence.workspaceKey.id, BASIC_CANARY_KEY_ID);
  assert.equal(result.writeCounts.workspaceKeysCreated, 1);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/keys" && call.search === "?page=2&pageSize=20"), true);
  assert.equal(fixture.state.modelRequests, 1);
});

test("customer Basic canary splits VPC preparation from hosted browser completion without replaying business writes", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "opl-basic-canary-split-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const checkpointPath = join(directory, "checkpoint.json");
  const fixture = basicCanaryFixture();

  const prepared = await productionLiveQa.verifyProductionBasicCustomerCanary({
    ...basicCanaryOptions(fixture),
    phase: "prepare",
    checkpointPath
  });

  assert.equal(prepared.ok, true);
  assert.equal(prepared.status, "prepared");
  assert.equal(prepared.stage, "runtime_ready");
  assert.equal(fixture.state.provisionPosts, 1);
  assert.equal(fixture.state.rechargePosts, 1);
  assert.equal(fixture.state.launchPosts, 1);
  assert.equal(fixture.state.modelRequests, 0);
  assert.equal(fixture.calls.some((call) => call.path.endsWith("/runtime-credentials/reveal")), false);
  assert.doesNotMatch(JSON.stringify(prepared), /password|token|secret|redeem|providerRequestId/i);

  const fabricCallsBeforeCompletion = fixture.calls.filter((call) => call.host === "fabric.opl-cloud.svc:8082").length;
  const result = await productionLiveQa.verifyProductionBasicCustomerCanary({
    ...basicCanaryOptions(fixture),
    phase: "complete",
    preparedEvidence: prepared,
    checkpointPath,
    fabricOrigin: undefined,
    internalServiceToken: undefined,
    fabricFetchImpl: async () => { throw new Error("hosted_completion_must_not_call_internal_fabric"); },
    cloudRevisionEvidenceReader: undefined,
    runtimePodEvidenceReader: undefined
  });

  assert.equal(result.status, "passed");
  assert.deepEqual({
    account: fixture.state.provisionPosts,
    wallet: fixture.state.rechargePosts,
    launch: fixture.state.launchPosts,
    model: fixture.state.modelRequests
  }, { account: 1, wallet: 1, launch: 1, model: 1 });
  assert.equal(fixture.calls.filter((call) => call.host === "fabric.opl-cloud.svc:8082").length, fabricCallsBeforeCompletion);
});

test("customer Basic canary split phases preserve zero recovered account and wallet POSTs", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "opl-basic-canary-recovered-split-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const checkpointPath = join(directory, "checkpoint.json");
  const fixture = basicCanaryFixture({
    initialProvisioned: true,
    initialRecharged: true,
    rechargeUsdMicros: 60_000_000
  });
  const options = basicCanaryOptions(fixture, { rechargeUsdMicros: "60000000" });

  const prepared = await productionLiveQa.verifyProductionBasicCustomerCanary({
    ...options,
    phase: "prepare",
    checkpointPath
  });

  assert.deepEqual(prepared.writeCounts, {
    accountProvisionPosts: 0,
    walletAdjustmentPosts: 0,
    workspaceLaunchPosts: 1,
    modelRequests: 0,
    workspaceKeysCreated: 1,
    workspacePurchaseDebits: 1,
    tencentCvmPurchases: 1,
    tencentCbsPurchases: 1
  });

  const result = await productionLiveQa.verifyProductionBasicCustomerCanary({
    ...options,
    phase: "complete",
    preparedEvidence: prepared,
    checkpointPath,
    fabricOrigin: undefined,
    internalServiceToken: undefined,
    fabricFetchImpl: async () => { throw new Error("hosted_completion_must_not_call_internal_fabric"); },
    cloudRevisionEvidenceReader: undefined,
    runtimePodEvidenceReader: undefined
  });

  assert.equal(result.status, "passed");
  assert.equal(result.writeCounts.accountProvisionPosts, 0);
  assert.equal(result.writeCounts.walletAdjustmentPosts, 0);
  assert.equal(result.writeCounts.workspaceLaunchPosts, 1);
  assert.equal(fixture.state.provisionPosts, 0);
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 1);
  assert.equal(fixture.state.modelRequests, 1);
});

test("customer Basic canary recovery completes split phases from the same historical precharge", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "opl-basic-canary-precharge-recovery-split-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const checkpointPath = join(directory, "checkpoint.json");
  const fixture = basicCanaryFixture({
    initialProvisioned: true,
    initialRecharged: true,
    rechargeUsdMicros: 60_000_000
  });
  const options = recoveredPrechargeBasicCanaryOptions(fixture);

  const prepared = await productionLiveQa.verifyProductionBasicCustomerCanary({
    ...options,
    phase: "prepare",
    checkpointPath
  });

  assert.equal(prepared.status, "prepared");
  assert.deepEqual(prepared.wallet, {
    operationId: BASIC_CANARY_WALLET_OPERATION_ID,
    source: "wallet_adjustment_authoritative_readback",
    beforeUsdMicros: "0",
    afterUsdMicros: "60000000",
    deltaUsdMicros: "60000000"
  });
  assert.deepEqual(prepared.httpAttempts, { workspaceLaunch: null, modelRequest: null });
  assert.equal(prepared.writeCounts.accountProvisionPosts, 0);
  assert.equal(prepared.writeCounts.walletAdjustmentPosts, 0);
  assert.equal(prepared.writeCounts.workspaceLaunchPosts, 1);

  const result = await productionLiveQa.verifyProductionBasicCustomerCanary({
    ...options,
    phase: "complete",
    preparedEvidence: prepared,
    checkpointPath,
    fabricOrigin: undefined,
    internalServiceToken: undefined,
    fabricFetchImpl: async () => { throw new Error("hosted_completion_must_not_call_internal_fabric"); },
    cloudRevisionEvidenceReader: undefined,
    runtimePodEvidenceReader: undefined
  });

  assert.equal(result.status, "passed");
  assert.equal(result.writeCounts.accountProvisionPosts, 0);
  assert.equal(result.writeCounts.walletAdjustmentPosts, 0);
  assert.equal(result.writeCounts.workspaceLaunchPosts, 1);
  assert.equal(fixture.calls.filter((call) => call.path.includes("wallet-adjustments") && call.method !== "GET").length, 0);
  assert.equal(fixture.calls.filter((call) => call.path === `/api/workspace-launches/${BASIC_CANARY_LAUNCH_OPERATION_ID}` && call.method === "GET").length > 0, true);
});

test("customer Basic canary recovery reads the approved launch identity after a lost launch response without a second POST", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "opl-basic-canary-precharge-launch-recovery-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const checkpointPath = join(directory, "checkpoint.json");
  const fixture = basicCanaryFixture({
    initialProvisioned: true,
    initialRecharged: true,
    rechargeUsdMicros: 60_000_000,
    loseResponseAfter: "launch"
  });
  const options = recoveredPrechargeBasicCanaryOptions(fixture);

  await assert.rejects(
    () => productionLiveQa.verifyProductionBasicCustomerCanary({ ...options, checkpointPath }),
    /simulated_launch_response_lost/
  );
  assert.equal(fixture.state.launchPosts, 1);

  const result = await productionLiveQa.verifyProductionBasicCustomerCanary({ ...options, checkpointPath });
  assert.equal(result.status, "passed");
  assert.equal(result.operationId, BASIC_CANARY_LAUNCH_OPERATION_ID);
  assert.equal(result.workspaceId, BASIC_CANARY_WORKSPACE_ID);
  assert.equal(fixture.state.launchPosts, 1);
  assert.equal(fixture.calls.filter((call) => call.path === "/api/workspace-launches" && call.method === "POST").length, 1);
  assert.equal(fixture.calls.filter((call) => call.path === `/api/workspace-launches/${BASIC_CANARY_LAUNCH_OPERATION_ID}` && call.method === "GET").length > 0, true);
});

for (const [failureAtRead, expectedPosts] of [
  [2, { account: 0, wallet: 0, launch: 0, model: 0 }],
  [3, { account: 1, wallet: 0, launch: 0, model: 0 }],
  [4, { account: 1, wallet: 1, launch: 0, model: 0 }],
  [5, { account: 1, wallet: 1, launch: 1, model: 0 }]
]) {
  test(`customer Basic canary rechecks immutable Cloud revision before business write ${failureAtRead - 1}`, async () => {
    const fixture = basicCanaryFixture();
    const original = fixture.cloudRevisionEvidenceReader;
    let reads = 0;
    fixture.cloudRevisionEvidenceReader = async (input) => {
      reads += 1;
      if (reads === failureAtRead) throw new Error("production_basic_canary_cloud_revision_invalid:changed_mid_run");
      return original(input);
    };

    await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_cloud_revision_invalid/);
    assert.deepEqual({
      account: fixture.state.provisionPosts,
      wallet: fixture.state.rechargePosts,
      launch: fixture.state.launchPosts,
      model: fixture.state.modelRequests
    }, expectedPosts);
  });
}

test("customer Basic canary rejects every Cloud service revision mismatch before all business POSTs", async () => {
  for (const component of ["control-plane", "fabric", "ledger"]) {
    const fixture = basicCanaryFixture({ cloudRevisionError: `production_basic_canary_cloud_revision_invalid:${component}` });
    await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_cloud_revision_invalid/);
    assert.deepEqual({
      account: fixture.state.provisionPosts,
      wallet: fixture.state.rechargePosts,
      launch: fixture.state.launchPosts,
      model: fixture.state.modelRequests
    }, { account: 0, wallet: 0, launch: 0, model: 0 }, component);
    assert.equal(fixture.calls.length, 0, component);
  }
});

for (const [name, initial, expectedPosts] of [
  ["account", { initialProvisioned: true }, { provision: 0, recharge: 1, launch: 1 }],
  ["wallet", { initialProvisioned: true, initialRecharged: true }, { provision: 0, recharge: 0, launch: 1 }],
  ["launch", { initialProvisioned: true, initialRecharged: true, initialLaunchStatus: "fulfilling_compute" }, { provision: 0, recharge: 0, launch: 0 }]
]) {
  test(`customer Basic canary recovers ${name} authority with no checkpoint`, async () => {
    const fixture = basicCanaryFixture(initial);
    const result = await productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture));

    assert.equal(result.status, "passed");
    assert.deepEqual({
      provision: fixture.state.provisionPosts,
      recharge: fixture.state.rechargePosts,
      launch: fixture.state.launchPosts
    }, expectedPosts);
    assert.equal(fixture.state.modelRequests, 1);
    assert.equal(fixture.calls.some((call) => call.path === "/api/operator/accounts" && call.method === "GET"), true);
    assert.equal(fixture.calls.some((call) => call.path === `/api/operator/wallet-adjustments/${BASIC_CANARY_WALLET_OPERATION_ID}` && call.method === "GET"), true);
    assert.equal(fixture.calls.some((call) => call.path === `/api/workspace-launches/${BASIC_CANARY_LAUNCH_OPERATION_ID}` && call.method === "GET"), true);
  });
}

for (const stage of ["account_provision_attempted", "wallet_adjustment_attempted", "workspace_launch_attempted"]) {
  test(`customer Basic canary recovers authoritative absence after ${stage}`, async (t) => {
    const directory = await mkdtemp(join(tmpdir(), "opl-basic-canary-attempted-"));
    t.after(() => rm(directory, { recursive: true, force: true }));
    const checkpointPath = join(directory, "checkpoint.json");
    const fixture = basicCanaryFixture();
    let interrupted = false;

    await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary({
      ...basicCanaryOptions(fixture),
      checkpointPath,
      afterCheckpoint: async (checkpointStage) => {
        if (!interrupted && checkpointStage === stage) {
          interrupted = true;
          throw new Error(`simulated_exit_after_${stage}`);
        }
      }
    }), new RegExp(`simulated_exit_after_${stage}`));

    const result = await productionLiveQa.verifyProductionBasicCustomerCanary({ ...basicCanaryOptions(fixture), checkpointPath });
    assert.equal(result.status, "passed");
    assert.deepEqual({
      account: fixture.state.provisionPosts,
      wallet: fixture.state.rechargePosts,
      launch: fixture.state.launchPosts,
      model: fixture.state.modelRequests
    }, { account: 1, wallet: 1, launch: 1, model: 1 });
  });
}

for (const [name, expectedReadbackPath] of [
  ["account", "/api/operator/accounts"],
  ["wallet", `/api/operator/wallet-adjustments/${BASIC_CANARY_WALLET_OPERATION_ID}`],
  ["launch", `/api/workspace-launches/${BASIC_CANARY_LAUNCH_OPERATION_ID}`]
]) {
  test(`customer Basic canary recovers ${name} after the service commits and the response is lost`, async (t) => {
    const directory = await mkdtemp(join(tmpdir(), "opl-basic-canary-response-lost-"));
    t.after(() => rm(directory, { recursive: true, force: true }));
    const checkpointPath = join(directory, "checkpoint.json");
    const fixture = basicCanaryFixture({ loseResponseAfter: name });

    await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary({
      ...basicCanaryOptions(fixture),
      checkpointPath
    }), new RegExp(`simulated_${name}_response_lost`));
    const callsBeforeRecovery = fixture.calls.length;

    const result = await productionLiveQa.verifyProductionBasicCustomerCanary({
      ...basicCanaryOptions(fixture),
      checkpointPath
    });
    assert.equal(result.status, "passed");
    assert.deepEqual({
      account: fixture.state.provisionPosts,
      wallet: fixture.state.rechargePosts,
      launch: fixture.state.launchPosts,
      model: fixture.state.modelRequests
    }, { account: 1, wallet: 1, launch: 1, model: 1 });
    assert.equal(fixture.calls.slice(callsBeforeRecovery).some((call) => call.path === expectedReadbackPath && call.method === "GET"), true);
  });
}

test("customer Basic canary treats model_request_attempted as unknown after authoritative recovery and never resends", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "opl-basic-canary-model-attempted-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const checkpointPath = join(directory, "checkpoint.json");
  const fixture = basicCanaryFixture();

  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary({
    ...basicCanaryOptions(fixture),
    checkpointPath,
    afterCheckpoint: async (stage) => {
      if (stage === "model_request_attempted") throw new Error("simulated_exit_after_model_request_attempted");
    }
  }), /simulated_exit_after_model_request_attempted/);
  const callsBeforeRecovery = fixture.calls.length;

  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary({
    ...basicCanaryOptions(fixture),
    checkpointPath
  }), /production_basic_canary_model_result_unknown/);
  assert.equal(fixture.state.modelRequests, 0);
  assert.equal(fixture.calls.slice(callsBeforeRecovery).some((call) => call.path === `/api/workspace-launches/${BASIC_CANARY_LAUNCH_OPERATION_ID}` && call.method === "GET"), true);
  assert.equal(fixture.calls.slice(callsBeforeRecovery).some((call) => call.path === `/api/operator/wallet-adjustments/${BASIC_CANARY_WALLET_OPERATION_ID}` && call.method === "GET"), true);
});

test("customer Basic canary without a checkpoint fails model_result_unknown for an already Ready Workspace", async () => {
  const fixture = basicCanaryFixture({ initialProvisioned: true, initialRecharged: true, initialLaunchStatus: "succeeded" });

  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_model_result_unknown/);
  assert.deepEqual({
    account: fixture.state.provisionPosts,
    wallet: fixture.state.rechargePosts,
    launch: fixture.state.launchPosts,
    model: fixture.state.modelRequests
  }, { account: 0, wallet: 0, launch: 0, model: 0 });
});

for (const stage of ["account_provisioned", "wallet_recharged", "launch_accepted", "runtime_ready"]) {
  test(`customer Basic canary resumes after ${stage} without a second business mutation`, async (t) => {
    const directory = await mkdtemp(join(tmpdir(), "opl-basic-canary-"));
    t.after(() => rm(directory, { recursive: true, force: true }));
    const checkpointPath = join(directory, "checkpoint.json");
    const fixture = basicCanaryFixture();
    let interrupted = false;

    await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary({
      ...basicCanaryOptions(fixture),
      checkpointPath,
      afterCheckpoint: async (checkpointStage) => {
        if (!interrupted && checkpointStage === stage) {
          interrupted = true;
          throw new Error(`simulated_exit_after_${stage}`);
        }
      }
    }), new RegExp(`simulated_exit_after_${stage}`));

    const result = await productionLiveQa.verifyProductionBasicCustomerCanary({
      ...basicCanaryOptions(fixture),
      checkpointPath
    });
    assert.equal(result.status, "passed");
    assert.deepEqual({
      accountProvisionPosts: fixture.state.provisionPosts,
      walletAdjustmentPosts: fixture.state.rechargePosts,
      workspaceLaunchPosts: fixture.state.launchPosts,
      modelRequests: fixture.state.modelRequests
    }, {
      accountProvisionPosts: 1,
      walletAdjustmentPosts: 1,
      workspaceLaunchPosts: 1,
      modelRequests: 1
    });
    assert.deepEqual(result.httpAttempts, {
      accountProvision: null,
      walletAdjustment: null,
      workspaceLaunch: null,
      modelRequest: null
    });
    const checkpoint = await readFile(checkpointPath, "utf8");
    assert.doesNotMatch(checkpoint, /password|token|secret|redeem|providerRequestId/i);
    assert.match(checkpoint, /"approvalDigest": "[0-9a-f]{64}"/);
  });
}

test("customer Basic canary stops on manual review without replaying launch or sending a model request", async () => {
  const fixture = basicCanaryFixture({ terminalStatus: "manual_review" });
  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_manual_review/);
  assert.equal(fixture.state.launchPosts, 1);
  assert.equal(fixture.state.modelRequests, 0);
  assert.equal(fixture.calls.filter((call) => call.path === "/api/workspace-launches" && call.method === "POST").length, 1);
});

test("customer Basic canary rejects storage truth without an authoritative provider identity", async () => {
  const fixture = basicCanaryFixture({ truthStorageProviderId: "" });
  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_provider_truth_invalid/);
  assert.equal(fixture.state.launchPosts, 1);
  assert.equal(fixture.state.modelRequests, 0);
});

test("customer Basic canary requires a release-owner resolved instance type before network access", async () => {
  const fixture = basicCanaryFixture();
  const approval = JSON.parse(basicCanaryApprovalJson());
  delete approval.expected.resolvedInstanceType;

  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary({
    ...basicCanaryOptions(fixture),
    approvalJson: JSON.stringify(approval)
  }), /production_basic_canary_approval_invalid/);
  assert.equal(fixture.calls.length, 0);
});

test("customer Basic canary fails before writes unless Fabric catalog proves 2C and 4GiB", async () => {
  const fixture = basicCanaryFixture({ basicMemoryGb: 8 });

  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_resource_contract_invalid/);
  assert.equal(fixture.state.provisionPosts, 0);
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 0);
  assert.equal(fixture.state.modelRequests, 0);
});

test("customer Basic canary fails before writes unless Fabric catalog proves 10GiB", async () => {
  const fixture = basicCanaryFixture({ basicDiskGb: 20 });

  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_resource_contract_invalid/);
  assert.equal(fixture.state.provisionPosts, 0);
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 0);
  assert.equal(fixture.state.modelRequests, 0);
});

test("customer Basic canary fails closed unless Fabric allocation truth proves 2C and 4GiB", async () => {
  const fixture = basicCanaryFixture({ allocationMemoryGb: 8 });

  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_compute_allocation_invalid/);
  assert.equal(fixture.state.launchPosts, 1);
  assert.equal(fixture.state.modelRequests, 0);
});

test("customer Basic canary requires the Runtime Pod on the allocated and owned Machine node", async () => {
  const fixture = basicCanaryFixture({ podNodeName: "10.66.1.99" });

  await assert.rejects(() => productionLiveQa.verifyProductionBasicCustomerCanary(basicCanaryOptions(fixture)), /production_basic_canary_runtime_pod_invalid/);
  assert.equal(fixture.state.launchPosts, 1);
  assert.equal(fixture.state.modelRequests, 0);
});

test("customer Basic canary CLI requires every explicit write authorization before network access", async () => {
  let stderr = "";
  let calls = 0;
  const code = await runProductionLiveQaCli({
    argv: ["--basic-customer-canary", "--approval-id", BASIC_CANARY_APPROVAL_ID],
    env: {
      OPL_BASIC_CANARY_APPROVAL_JSON: basicCanaryApprovalJson(),
      OPL_BASIC_CANARY_CONFIRMATION: BASIC_CANARY_CONFIRMATION
    },
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(code, 1);
  assert.match(stderr, /production_basic_canary_write_allow_flags_required/);
  assert.equal(calls, 0);
});

test("customer Basic canary CLI invokes the same redacted orchestration", async () => {
  const fixture = basicCanaryFixture();
  let stdout = "";
  let stderr = "";
  const code = await runProductionLiveQaCli({
    argv: [
      "--basic-customer-canary",
      "--allow-account-provision",
      "--allow-wallet-recharge",
      "--allow-workspace-purchase",
      "--allow-model-write",
      "--approval-id", BASIC_CANARY_APPROVAL_ID
    ],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_FABRIC_INTERNAL_ORIGIN: "http://fabric.opl-cloud.svc:8082",
      OPL_INTERNAL_SERVICE_TOKEN: "internal-service-token",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD,
      OPL_BASIC_CANARY_CUSTOMER_PASSWORD: BASIC_CANARY_CUSTOMER_PASSWORD,
      OPL_BASIC_CANARY_APPROVAL_JSON: basicCanaryApprovalJson(),
      OPL_BASIC_CANARY_CONFIRMATION: BASIC_CANARY_CONFIRMATION,
      OPL_MERGED_SHA: BASIC_CANARY_MERGED_SHA,
      OPL_VERIFY_RUN_ID: "production-basic-canary-20260726",
      OPL_VERIFY_LAUNCH_POLL_ATTEMPTS: "3",
      OPL_VERIFY_LAUNCH_POLL_DELAY_MS: "0",
      OPL_VERIFY_USAGE_ATTEMPTS: "2",
      OPL_VERIFY_USAGE_RETRY_DELAY_MS: "0",
      OPL_VERIFY_BROWSER_TIMEOUT_MS: "20",
      OPL_VERIFY_MODEL_TIMEOUT_MS: "20"
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: fixture.fetchImpl,
    fabricFetchImpl: fixture.fetchImpl,
    browserFactory: fixture.browserFactory,
    cloudRevisionEvidenceReader: fixture.cloudRevisionEvidenceReader,
    runtimePodEvidenceReader: fixture.runtimePodEvidenceReader,
    now: new Date("2026-07-26T00:00:00Z")
  });
  assert.equal(code, 0, stderr);
  const result = JSON.parse(stdout);
  assert.equal(result.status, "passed");
  assert.equal(result.writeCounts.workspaceLaunchPosts, 1);
  assert.doesNotMatch(stdout, /customer-password|workspace-password|internal-service-token|must-not-emit|redeem/i);
});

test("customer Basic canary CLI accepts only the historical-precharge recovery authorization", async () => {
  const fixture = basicCanaryFixture({
    initialProvisioned: true,
    initialRecharged: true,
    rechargeUsdMicros: 60_000_000
  });
  let stdout = "";
  let stderr = "";
  const code = await runProductionLiveQaCli({
    argv: [
      "--basic-customer-canary",
      "--funding-mode", "operator_precharge_recovery",
      "--allow-existing-precharge-recovery",
      "--allow-workspace-purchase",
      "--allow-model-write",
      "--approval-id", BASIC_CANARY_APPROVAL_ID
    ],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_FABRIC_INTERNAL_ORIGIN: "http://fabric.opl-cloud.svc:8082",
      OPL_INTERNAL_SERVICE_TOKEN: "internal-service-token",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD,
      OPL_BASIC_CANARY_CUSTOMER_PASSWORD: BASIC_CANARY_CUSTOMER_PASSWORD,
      OPL_BASIC_CANARY_APPROVAL_JSON: recoveredPrechargeBasicCanaryApprovalJson(),
      OPL_BASIC_CANARY_CONFIRMATION: BASIC_CANARY_CONFIRMATION,
      OPL_MERGED_SHA: BASIC_CANARY_MERGED_SHA,
      OPL_VERIFY_RUN_ID: "production-basic-canary-precharge-recovery",
      OPL_VERIFY_LAUNCH_POLL_ATTEMPTS: "3",
      OPL_VERIFY_LAUNCH_POLL_DELAY_MS: "0",
      OPL_VERIFY_USAGE_ATTEMPTS: "2",
      OPL_VERIFY_USAGE_RETRY_DELAY_MS: "0",
      OPL_VERIFY_BROWSER_TIMEOUT_MS: "20",
      OPL_VERIFY_MODEL_TIMEOUT_MS: "20"
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: fixture.fetchImpl,
    fabricFetchImpl: fixture.fetchImpl,
    browserFactory: fixture.browserFactory,
    cloudRevisionEvidenceReader: fixture.cloudRevisionEvidenceReader,
    runtimePodEvidenceReader: fixture.runtimePodEvidenceReader,
    now: new Date("2026-07-26T00:00:00Z")
  });

  assert.equal(code, 0, stderr);
  const result = JSON.parse(stdout);
  assert.equal(result.status, "passed");
  assert.equal(result.writeCounts.accountProvisionPosts, 0);
  assert.equal(result.writeCounts.walletAdjustmentPosts, 0);
  assert.equal(result.writeCounts.workspaceLaunchPosts, 1);
  assert.equal(fixture.state.provisionPosts, 0);
  assert.equal(fixture.state.rechargePosts, 0);
  assert.equal(fixture.state.launchPosts, 1);
});

function readOnlyFixture({
  healthStatus = 200,
  noWorkspace = false,
  adminHasWorkspace = !noWorkspace,
  authMe = {},
  operatorAccountsItems,
  readiness = { ready: false, cloudImagesReady: true, workspaceImagesReady: false, immutableImagesReady: false }
} = {}) {
  const calls = [];
  const viewports = [];
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    calls.push({ method, path: url.pathname, search: url.search });
    if (url.pathname === "/api/healthz") return json({ status: "ok" }, healthStatus);
    if (url.pathname === "/api/production/readiness") return json(readiness);
    if (url.pathname === "/api/auth/login") {
      assert.deepEqual(JSON.parse(init.body), { email: ADMIN_EMAIL, password: ADMIN_PASSWORD });
      return json({ user: { id: ADMIN_USER_ID, accountId: ADMIN_ACCOUNT_ID, role: "admin" } }, 200, {
        "set-cookie": "opl_session=session-read-only; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-read-only"
      });
    }
    if (["/api/projects", "/api/execution-requests", "/api/workspaces/retired/resume"].includes(url.pathname)) {
      return json({ error: "not_found" }, 404, { "content-security-policy": "default-src 'self'" });
    }
    const headers = new Headers(init.headers);
    assert.match(headers.get("cookie") || "", /opl_session=session-read-only/);
    if (url.pathname === "/api/auth/me") {
      return source({
        consoleUserId: ADMIN_USER_ID,
        accountId: ADMIN_ACCOUNT_ID,
        role: "admin",
        sub2apiUserId: "41",
        email: ADMIN_EMAIL,
        status: "active",
        ...authMe
      });
    }
    if (url.pathname === "/api/gateway/endpoint") return source({ baseUrl: "https://gflabtoken.cn/v1" });
    if (url.pathname === "/api/gateway/wallet") return source({ userId: "41", currency: "USD", usdMicros: "500000000", status: "active" });
    if (url.pathname === "/api/gateway/keys") {
      return source({ items: [{ id: "9", name: "admin-key", status: "active" }], total: 1, page: 1, pageSize: 20, pages: 1 });
    }
    if (url.pathname === "/api/gateway/keys/9/usage") {
      return source({ items: [], total: 0, page: 1, pageSize: 20, pages: 1 }, "sub2api", "empty");
    }
    if (url.pathname === "/api/gateway/balance-history") {
      return source({ items: [], total: 0, page: 1, pageSize: 20, pages: 1 }, "sub2api", "empty");
    }
    if (url.pathname === "/api/operator/overview") {
      return source({
        accounts: nestedSource({ total: 1000 }, "control-plane"),
        workspaces: nestedSource({ total: noWorkspace ? 0 : 1 }, "control-plane"),
        resources: nestedSource({ total: noWorkspace ? 0 : 1 }, "fabric")
      }, "control-plane");
    }
    if (url.pathname === "/api/operator/accounts") {
      return source({
        items: operatorAccountsItems || [{ accountId: ADMIN_ACCOUNT_ID, consoleUserId: ADMIN_USER_ID }],
        total: 1000,
        page: 1,
        pageSize: 20
      }, "control-plane+sub2api");
    }
    if (url.pathname === "/api/operator/workspaces") {
      return source({ items: noWorkspace ? [] : [{ id: "workspace-current" }], total: noWorkspace ? 0 : 1, page: 1, pageSize: 20 }, "control-plane+fabric+sub2api", noWorkspace ? "empty" : "available");
    }
    if (url.pathname === "/api/workspaces") {
      return source({ items: adminHasWorkspace ? [{ id: "workspace-current", runtimeUrl: "https://workspace.medopl.cn/w/workspace-current/" }] : [], total: adminHasWorkspace ? 1 : 0, page: 1, pageSize: 20 }, "control-plane", adminHasWorkspace ? "available" : "empty");
    }
    if (url.pathname === "/api/workspaces/workspace-current/runtime-status") {
      return source({ workspaceId: "workspace-current", ready: true, url: "https://workspace.medopl.cn/w/workspace-current/" }, "fabric");
    }
    if (url.pathname === "/api/billing/receipts") {
      return source({ receipts: [], nextCursor: "", hasMore: false }, "ledger", "empty");
    }
    return json({ error: "not_found" }, 404);
  };
  return { calls, viewports, fetchImpl, browserFactory: readOnlyBrowserFactory(viewports) };
}

function liveFixture({
  changedResourceIds = false,
  changedProviderOperations = false,
  changedLaunchOperation = false,
  changedRuntimeOperation = false,
  changedUntrackedOperation = false,
  changedMutationAction = "",
  changedReceipt = false,
  frames = true,
  responseSuffix = "",
  slotMissing = false,
  usageStuck = false,
  ambiguousUsage = false,
  invalidUsageRecord = false,
  usageOverrides = {},
  usageSnapshotTooLarge = false,
  emptyUsageBaseline = false,
  statsStuck = false,
  balanceMismatch = false,
  usageKeyId = "9",
  duplicateKey = false,
  statusLeaksPassword = false,
  revealCacheControl = "private, no-store"
} = {}) {
  const state = { modelRequests: 0, stateReads: 0 };
  const calls = [];
  const deadline = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString();
  const periodStart = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();
  const liveUsage = {
    apiKeyId: usageKeyId, requestId: "req-rollout-qa-1", createdAt: new Date().toISOString(), model: "gpt-5.5", inboundEndpoint: "/v1/responses", requestType: "sync",
    inputTokens: 8, outputTokens: 1, cacheCreationTokens: 0, cacheReadTokens: 0, actualCostUsdMicros: invalidUsageRecord ? 12.5 : 120,
    ...usageOverrides
  };
  const usageItems = () => {
    const items = emptyUsageBaseline ? [] : [{
      apiKeyId: "9", requestId: "req-before-1", createdAt: periodStart, model: "gpt-5.5", inboundEndpoint: "/v1/responses", requestType: "sync",
      inputTokens: 10, outputTokens: 5, cacheCreationTokens: 0, cacheReadTokens: 0, actualCostUsdMicros: 100
    }];
    if (state.modelRequests > 0 && !usageStuck) {
      items.unshift(liveUsage);
      if (ambiguousUsage) items.unshift({ ...liveUsage, requestId: "req-concurrent-2" });
    }
    return items;
  };
  const resourceState = () => {
    state.stateReads += 1;
    const suffix = changedResourceIds && state.stateReads > 1 ? "changed" : "1";
    const result = {
      computeAllocations: [{
        id: "compute-slot-1",
        accountId: BASIC_ACCOUNT_ID,
        workspaceId: "workspace-slot-1",
        providerResourceId: "ins-slot-1",
        nodePoolId: `np-slot-${suffix}`,
        status: "running",
        costTags: { opl_account_id: BASIC_ACCOUNT_ID, opl_workspace_id: "workspace-slot-1", opl_resource_id: "compute-slot-1" },
        providerData: { instanceType: "SA5.MEDIUM4", zone: "ap-guangzhou-3", chargeType: "PREPAID", periodMonths: "1", renewFlag: "NOTIFY_AND_MANUAL_RENEW", deadline }
      }],
      storageVolumes: [{
        id: "storage-slot-1",
        accountId: BASIC_ACCOUNT_ID,
        workspaceId: "workspace-slot-1",
        providerResourceId: "disk-slot-1",
        sizeGb: 10,
        status: "available",
        costTags: { opl_account_id: BASIC_ACCOUNT_ID, opl_workspace_id: "workspace-slot-1", opl_resource_id: "storage-slot-1" },
        providerData: { diskChargeType: "PREPAID", periodMonths: "1", renewFlag: "NOTIFY_AND_MANUAL_RENEW", deadline, zone: "ap-guangzhou-3", pvName: "pv-slot-1" }
      }],
      workspaces: [{
        id: "workspace-slot-1",
        accountId: BASIC_ACCOUNT_ID,
        ownerAccountId: BASIC_ACCOUNT_ID,
        verificationSlotId: "verification-slot-basic-01",
        customerProduct: false,
        currentComputeAllocationId: "compute-slot-1",
        storageId: "storage-slot-1",
        state: "running",
        openable: true,
        receiptId: changedReceipt && state.stateReads > 1 ? "receipt-current-2" : "receipt-current-1",
        url: "https://workspace.medopl.cn/w/workspace-slot-1/"
      }],
      runtimeOperations: [
        { id: "provider-op-compute-1", accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: "create_compute_allocation", status: "succeeded", providerRequestId: "ins-slot-1", result: '{"resource":"compute-slot-1"}' },
        { id: "provider-op-storage-1", accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: "create_storage_volume", status: "succeeded", providerRequestId: "disk-slot-1", result: '{"resource":"storage-slot-1"}' },
        { id: "workspace-launch-1", accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: "workspace.launch", status: "succeeded", providerRequestId: "",
          result: changedLaunchOperation && state.stateReads > 1 ? '{"phase":"changed"}' : '{"phase":"completed","credential":"must-not-emit"}' },
        { id: "workspace-renewal-1", accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: "workspace.renewal", status: "succeeded", providerRequestId: "",
          result: changedRuntimeOperation && state.stateReads > 1 ? '{"phase":"changed"}' : '{"phase":"completed"}' },
        { id: "job-progress-1", accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: "job.execute", status: changedUntrackedOperation && state.stateReads > 1 ? "succeeded" : "running",
          result: { internalCredential: "ignored-job-secret" } },
        ...(changedProviderOperations && state.stateReads > 1
          ? [{ id: "provider-op-renew-2", accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: "renew_compute_allocation", status: "succeeded", providerRequestId: "ins-slot-1", result: '{"resource":"compute-slot-1"}' }]
          : []),
        ...(changedMutationAction && state.stateReads > 1
          ? [{ id: `mutation-${changedMutationAction.replaceAll(".", "-")}`, accountId: BASIC_ACCOUNT_ID, workspaceId: "workspace-slot-1", action: changedMutationAction, status: "succeeded", providerRequestId: "provider-mutation-1", result: "{}" }]
          : [])
      ]
    };
    if (slotMissing) {
      result.computeAllocations = [];
      result.storageVolumes = [];
      result.workspaces = [];
    }
    return result;
  };

  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    const headers = new Headers(init.headers);
    calls.push({ method, path: url.pathname, search: url.search, signal: init.signal });
    if (url.hostname === "workspace.medopl.cn") return new Response("<main>workspace</main>", { status: 200 });
    if (url.pathname === "/api/production/readiness") return json({ ready: true, cloudImagesReady: true, workspaceImagesReady: true, immutableImagesReady: true });
    if (url.pathname === "/api/auth/login") {
      return json({ user: { accountId: BASIC_ACCOUNT_ID, role: "owner" } }, 200, {
        "set-cookie": "opl_session=session-alpha; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-alpha"
      });
    }
    assert.match(headers.get("cookie") || "", /opl_session=session-alpha/);
    if (url.pathname === "/api/pricing/catalog") {
      return json({
        priceVersion: "pilot-usd-2026-07-v1", currency: "USD", displayCurrency: "USD", walletCurrency: "USD",
        storagePer10GbMonthly: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", displayCurrency: "USD", usdMicros: 2_580_000 },
        packages: [
          { id: "basic", price: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", displayCurrency: "USD", chargeUsdMicros: 50_000_000 } },
          { id: "pro", price: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", displayCurrency: "USD", chargeUsdMicros: 214_280_000 } }
        ]
      });
    }
    if (url.pathname === "/api/state") return json(resourceState());
    if (url.pathname === "/api/gateway/wallet") {
      const charged = state.modelRequests > 0 && !usageStuck;
      const delta = charged ? liveUsage.actualCostUsdMicros + (balanceMismatch ? 1 : 0) : 0;
      return source({ userId: "41", currency: "USD", usdMicros: String(500_000_000 - delta), status: "active" });
    }
    if (url.pathname === "/api/gateway/keys") {
      const keys = [{ id: "9", name: "opl-workspace", status: "active", quotaUsdMicros: 1_000_000, quotaUsedUsdMicros: 1_000 }];
      if (duplicateKey) keys.push({ ...keys[0], id: "10" });
      return source({ items: keys, total: keys.length });
    }
    if (url.pathname === "/api/gateway/keys/9/usage") {
      if (usageSnapshotTooLarge) return source({ items: [], total: 10_001, page: 1, pageSize: 100, pages: 101 });
      const items = usageItems();
      const page = Number(url.searchParams.get("page") || 1);
      const pageSize = Number(url.searchParams.get("pageSize") || 50);
      return source({ items: items.slice((page - 1) * pageSize, page * pageSize), total: items.length, page, pageSize, pages: items.length === 0 ? 0 : Math.ceil(items.length / pageSize) }, "sub2api", items.length === 0 ? "empty" : "available");
    }
    if (url.pathname === "/api/gateway/keys/9/usage-summary") {
      const includeLive = state.modelRequests > 0 && !usageStuck && !statsStuck;
      const count = includeLive ? (ambiguousUsage ? 2 : 1) : 0;
      const baselineRequests = emptyUsageBaseline ? 0 : 1;
      const baselineInputTokens = emptyUsageBaseline ? 0 : 10;
      const baselineOutputTokens = emptyUsageBaseline ? 0 : 5;
      const baselineCost = emptyUsageBaseline ? 0 : 100;
      return source({
        totalRequests: baselineRequests + count,
        totalInputTokens: baselineInputTokens + count * liveUsage.inputTokens,
        totalOutputTokens: baselineOutputTokens + count * liveUsage.outputTokens,
        totalTokens: baselineInputTokens + baselineOutputTokens + count * (liveUsage.inputTokens + liveUsage.outputTokens + liveUsage.cacheCreationTokens + liveUsage.cacheReadTokens),
        totalActualCostUsdMicros: baselineCost + count * liveUsage.actualCostUsdMicros
      });
    }
    if (/^\/api\/billing\/receipts\/receipt-current-[12]$/.test(url.pathname)) {
      return source({
        receiptId: url.pathname.endsWith("-2") ? "receipt-current-2" : "receipt-current-1",
        type: "workspace.created", status: "completed", workspaceId: "workspace-slot-1", createdAt: periodStart
      }, "ledger");
    }
    if (url.pathname === "/api/workspaces/workspace-slot-1/runtime-status") {
      assert.equal(method, "GET");
      assert.equal(init.body, undefined);
      return source({
        ready: true,
        url: "https://workspace.medopl.cn/w/workspace-slot-1/",
        access: { username: "opl", credentialStatus: "configured", ...(statusLeaksPassword ? { password: "workspace-password" } : {}) }
      }, "fabric");
    }
    if (url.pathname === "/api/workspaces/workspace-slot-1/runtime-credentials/reveal") {
      assert.equal(method, "POST");
      assert.equal(headers.get("x-opl-csrf"), "csrf-alpha");
      assert.deepEqual(JSON.parse(init.body), {});
      return json({
        workspaceId: "workspace-slot-1",
        access: { username: "opl", password: "workspace-password", credentialStatus: "configured" }
      }, 200, { "cache-control": revealCacheControl });
    }
    return json({ error: "not_found" }, 404);
  };

  return { browserFactory: browserFactory(state, { frames, responseSuffix }), calls, fetchImpl, state };
}

function options(fixture) {
  return {
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    accountId: BASIC_ACCOUNT_ID,
    runId: "rollout-qa-1",
    confirmation: LIVE_QA_CONFIRMATION,
    slotDescriptor: fixedSlotDescriptor,
    workspaceUrlAttempts: 1,
    retryDelayMs: 0,
    usageAttempts: 2,
    usageRetryDelayMs: 0,
    browserTimeoutMs: 20,
    modelTimeoutMs: 20,
    expectedModel: "gpt-5.5",
    mutationApprovalJson,
    mutationApprovalId: "approval-production-verification",
    browserFactory: fixture.browserFactory,
    fetchImpl: fixture.fetchImpl
  };
}

test("rollout QA proves Workspace login, WebSocket frames, one model response, usage growth, and stable resource ids", async () => {
  const fixture = liveFixture();
  const result = await verifyProductionLiveQa(options(fixture));

  assert.equal(result.ok, true);
  assert.equal(result.workspace.login, true);
  assert.equal(result.workspace.authUser, true);
  assert.equal(result.workspace.websocket.status, 101);
  assert.equal(result.workspace.websocket.framesSent > 0, true);
  assert.equal(result.workspace.websocket.framesReceived > 0, true);
  assert.equal(result.workspace.modelResponse, true);
  assert.equal(result.usage.request.requestId, "req-rollout-qa-1");
  assert.equal(result.usage.request.apiKeyId, "9");
  assert.equal(result.usage.request.model, "gpt-5.5");
  assert.equal(result.usage.request.requestType, "sync");
  assert.equal(result.usage.request.inboundEndpoint, "/v1/responses");
  assert.equal(result.usage.request.inputTokens + result.usage.request.outputTokens > 0, true);
  assert.equal(result.usage.request.actualCostUsdMicros, 120);
  assert.equal(result.usage.stats.delta.totalRequests, 1);
  assert.equal(BigInt(result.balance.before.usdMicros) - BigInt(result.balance.after.usdMicros), 120n);
  assert.equal(result.ledgerReceipt.receiptId, "receipt-current-1");
  assert.equal(result.ledgerReceipt.type, "workspace.created");
  assert.equal(result.runtimeOperations.unchanged, true);
  assert.equal(result.resourceIds.unchanged, true);
  assert.deepEqual(result.resourceIds.before, result.resourceIds.after);
  assert.equal(fixture.state.modelRequests, 1);
  assert.doesNotMatch(JSON.stringify(result), /console-password|workspace-password|sk-\*\*\*\*|OPL_QA_|must-not-emit/);
  assert.equal(fixture.calls.some((call) => call.path === "/api/billing/receipts/receipt-current-1"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/billing/receipts"), false);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/summary" || /^\/api\/workspaces\/[^/]+\/receipt$/.test(call.path)), false);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/usage" || call.path === "/api/gateway/usage/stats" || call.path === "/api/workspaces/runtime-status"), false);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/keys/9/usage"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/keys/9/usage-summary"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/workspaces/workspace-slot-1/runtime-status"), true);
  assert.equal(fixture.calls.some((call) => /create|destroy|detach|renew/i.test(call.path)), false);
  assert.equal(fixture.calls.every((call) => call.signal instanceof AbortSignal), true);
});

test("rollout QA fails before the model request when WebSocket frames are missing", async () => {
  const fixture = liveFixture({ frames: false });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /workspace_websocket_frames_required/);
  assert.equal(fixture.state.modelRequests, 0);
});

test("rollout QA requires the model response to contain only the unique token", async () => {
  const fixture = liveFixture({ responseSuffix: " extra text" });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /workspace_model_response_required/);
  assert.equal(fixture.state.modelRequests, 1);
});

test("rollout QA reports Provider Acceptance without starting a browser when the fixed slot is absent", async () => {
  const fixture = liveFixture({ slotMissing: true });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /provider_acceptance_required/);
  assert.equal(fixture.state.modelRequests, 0);
});

test("rollout QA never retries the model request when usage does not increase", async () => {
  const fixture = liveFixture({ usageStuck: true });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /exact_gateway_request_not_found/);
  assert.equal(fixture.state.modelRequests, 1);
});

test("rollout QA fails closed unless exactly one new request id and matching stats appear", async () => {
  for (const [fixture, error] of [
    [liveFixture({ ambiguousUsage: true }), /gateway_request_cardinality_mismatch/],
    [liveFixture({ invalidUsageRecord: true }), /gateway_request_usage_invalid/],
    [liveFixture({ usageKeyId: "10" }), /gateway_request_usage_invalid/],
    [liveFixture({ balanceMismatch: true }), /gateway_balance_delta_mismatch/],
    [liveFixture({ statsStuck: true }), /gateway_usage_stats_mismatch/]
  ]) {
    await assert.rejects(() => verifyProductionLiveQa(options(fixture)), error);
    assert.equal(fixture.state.modelRequests, 1);
  }

  const duplicateKey = liveFixture({ duplicateKey: true });
  await assert.rejects(() => verifyProductionLiveQa(options(duplicateKey)), /dedicated_workspace_key_required/);
  assert.equal(duplicateKey.state.modelRequests, 0);
});

test("rollout QA accepts the Control Plane empty usage page before the one model request", async () => {
  const fixture = liveFixture({ emptyUsageBaseline: true });
  const result = await verifyProductionLiveQa(options(fixture));
  assert.equal(fixture.state.modelRequests, 1);
  assert.equal(result.usage.request.requestId, "req-rollout-qa-1");
  assert.equal(result.usage.stats.before.totalRequests, 0);
  assert.equal(result.usage.stats.delta.totalRequests, 1);
});

test("rollout QA requires the exact model request contract, positive cost, and a bounded usage snapshot", async () => {
  for (const fixture of [
    liveFixture({ usageOverrides: { model: "gpt-4.1" } }),
    liveFixture({ usageOverrides: { requestType: "stream" } }),
    liveFixture({ usageOverrides: { inboundEndpoint: "/v1/chat/completions" } }),
    liveFixture({ usageOverrides: { actualCostUsdMicros: 0 } })
  ]) {
    await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /gateway_request_usage_invalid/);
    assert.equal(fixture.state.modelRequests, 1);
  }

  const oversized = liveFixture({ usageSnapshotTooLarge: true });
  await assert.rejects(() => verifyProductionLiveQa(options(oversized)), /gateway_usage_snapshot_limit_exceeded/);
  assert.equal(oversized.state.modelRequests, 0);

  const missingModel = liveFixture();
  const missingModelOptions = options(missingModel);
  delete missingModelOptions.expectedModel;
  await assert.rejects(() => verifyProductionLiveQa(missingModelOptions), /production_live_qa_expected_model_required/);
  assert.equal(missingModel.calls.length, 0);
});

test("rollout QA obtains credentials only from private no-store reveal", async () => {
  await assert.rejects(() => verifyProductionLiveQa(options(liveFixture({ statusLeaksPassword: true }))), /runtime_status_secret_forbidden/);
  await assert.rejects(() => verifyProductionLiveQa(options(liveFixture({ revealCacheControl: "no-store" }))), /runtime_credentials_cache_control_invalid/);
});

test("rollout QA rejects any provider addition or same-id launch and renewal result change", async () => {
  for (const fixture of [
    liveFixture({ changedProviderOperations: true }),
    liveFixture({ changedLaunchOperation: true }),
    liveFixture({ changedRuntimeOperation: true })
  ]) {
    await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /production_live_qa_runtime_operations_changed/);
    assert.equal(fixture.state.modelRequests, 1);
  }
});

test("rollout QA rejects every provider write operation while ignoring read-only sync", async () => {
  for (const action of [
    "tag_compute_machine",
    "create_storage_attachment", "detach_storage_attachment",
    "create_workspace_runtime", "destroy_workspace_runtime",
    "upsert_gateway_secret", "workspace.gateway_secret.rotate",
    "create_storage_snapshot", "restore_storage_snapshot", "destroy_storage_snapshot"
  ]) {
    const fixture = liveFixture({ changedMutationAction: action });
    await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /production_live_qa_runtime_operations_changed/, action);
    assert.equal(fixture.state.modelRequests, 1);
  }
});

test("rollout QA rejects changes to any account RuntimeOperation without a static action allowlist", async () => {
  const fixture = liveFixture({ changedUntrackedOperation: true });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /production_live_qa_runtime_operations_changed/);
  assert.equal(fixture.state.modelRequests, 1);
});

test("rollout QA requires the same safe Workspace receipt before and after the request", async () => {
  const fixture = liveFixture({ changedReceipt: true });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /production_live_qa_ledger_receipt_changed/);
  assert.equal(fixture.state.modelRequests, 1);
});

test("rollout QA fails closed when any retained provider resource id changes", async () => {
  const fixture = liveFixture({ changedResourceIds: true });
  await assert.rejects(() => verifyProductionLiveQa(options(fixture)), /production_live_qa_resource_ids_changed/);
  assert.equal(fixture.state.modelRequests, 1);
});

test("rollout QA CLI requires explicit one-request confirmation before network access", async () => {
  let stderr = "";
  let calls = 0;
  const code = await runProductionLiveQaCli({
    argv: ["--allow-gateway-write", "--allow-model-write", "--approval-id", "approval-production-verification"],
    env: {
      OPL_VERIFY_ACCOUNT_ID: BASIC_ACCOUNT_ID,
      OPL_VERIFY_MUTATION_APPROVAL_JSON: mutationApprovalJson
    },
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(code, 1);
  assert.match(stderr, /production_live_qa_confirmation_required/);
  assert.equal(calls, 0);
});

test("rollout QA CLI rejects an invalid slot descriptor before network access", async () => {
  let stderr = "";
  let calls = 0;
  const code = await runProductionLiveQaCli({
    argv: ["--allow-gateway-write", "--allow-model-write", "--approval-id", "approval-production-verification"],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_VERIFY_AUTH_USERS_JSON: ownerSeed,
      OPL_VERIFY_ACCOUNT_ID: BASIC_ACCOUNT_ID,
      OPL_VERIFY_LIVE_QA_CONFIRMATION: LIVE_QA_CONFIRMATION,
      OPL_VERIFY_EXPECTED_MODEL: "gpt-5.5",
      OPL_VERIFY_MUTATION_APPROVAL_JSON: mutationApprovalJson,
      OPL_VERIFY_SLOT_DESCRIPTOR_JSON: "{"
    },
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(code, 1);
  assert.match(stderr, /verification_slot_descriptor_invalid/);
  assert.equal(calls, 0);
});

test("rollout QA read-only evidence level performs no model or Gateway write", async () => {
  let stdout = "";
  let stderr = "";
  const fixture = readOnlyFixture();
  const code = await runProductionLiveQaCli({
    argv: ["--read-only"],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: fixture.fetchImpl,
    browserFactory: fixture.browserFactory
  });
  assert.equal(code, 0, stderr);
  const result = JSON.parse(stdout);
  assert.equal(result.ok, true);
  assert.equal(result.mode, "read-only");
  assert.equal(result.evidenceLevel, "read-only");
  assert.equal(result.writesPerformed, 0);
  assert.deepEqual(result.checks.readiness, {
    cloudImagesReady: true,
    systemReady: false,
    workspaceImagesReady: false,
    immutableImagesReady: false
  });
  assert.deepEqual(result.viewports, ["desktop", "mobile"]);
  assert.deepEqual(fixture.viewports, [{ width: 1440, height: 900 }, { width: 390, height: 844 }]);
  assert.deepEqual(fixture.calls.filter((call) => call.method !== "GET").map(({ method, path }) => ({ method, path })), [{ method: "POST", path: "/api/auth/login" }]);
  assert.equal(fixture.calls.some((call) => call.path === "/api/healthz"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/production/readiness"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/auth/me"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/endpoint"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/keys" && call.search === "?page=1&pageSize=20"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/keys/9/usage" && call.search === "?page=1&pageSize=20"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/operator/overview"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/operator/accounts" && call.search === "?page=1&pageSize=20"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/operator/workspaces" && call.search === "?page=1&pageSize=20"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/workspaces/workspace-current/runtime-status"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/billing/receipts"), true);
  assert.equal(fixture.calls.filter((call) => ["/api/projects", "/api/execution-requests", "/api/workspaces/retired/resume"].includes(call.path)).length, 3);

  stderr = "";
  const failed = await runProductionLiveQaCli({
    argv: ["--read-only"],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD
    },
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: readOnlyFixture({ healthStatus: 503 }).fetchImpl,
    browserFactory: readOnlyBrowserFactory([])
  });
  assert.equal(failed, 1);
  assert.match(stderr, /request_failed:GET:\/api\/healthz:503/);

  stderr = "";
  const cloudImagesUnavailable = await runProductionLiveQaCli({
    argv: ["--read-only"],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD
    },
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: readOnlyFixture({ readiness: { ready: true, cloudImagesReady: false, workspaceImagesReady: true, immutableImagesReady: true } }).fetchImpl,
    browserFactory: readOnlyBrowserFactory([])
  });
  assert.equal(cloudImagesUnavailable, 1);
  assert.match(stderr, /production_cloud_readiness_invalid/);

  stderr = "";
  const identityMismatch = await runProductionLiveQaCli({
    argv: ["--read-only"],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD
    },
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: readOnlyFixture({ authMe: { accountId: "acct-other" } }).fetchImpl,
    browserFactory: readOnlyBrowserFactory([])
  });
  assert.equal(identityMismatch, 1);
  assert.match(stderr, /production_admin_identity_invalid/);

  stdout = "";
  stderr = "";
  const noWorkspaceFixture = readOnlyFixture({ noWorkspace: true });
  const noWorkspace = await runProductionLiveQaCli({
    argv: ["--read-only"],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: noWorkspaceFixture.fetchImpl,
    browserFactory: noWorkspaceFixture.browserFactory
  });
  assert.equal(noWorkspace, 0, stderr);
  assert.equal(JSON.parse(stdout).checks.fabric, "not_applicable_no_workspace");

  stdout = "";
  stderr = "";
  const adminOffFirstPageFixture = readOnlyFixture({
    operatorAccountsItems: [{ accountId: "acct-customer-0001", consoleUserId: "usr-customer-0001" }]
  });
  const adminOffFirstPage = await runProductionLiveQaCli({
    argv: ["--read-only"],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: adminOffFirstPageFixture.fetchImpl,
    browserFactory: adminOffFirstPageFixture.browserFactory
  });
  assert.equal(adminOffFirstPage, 0, stderr);
  assert.equal(JSON.parse(stdout).checks.identity, "authoritative");

  stdout = "";
  stderr = "";
  const globalWorkspaceFixture = readOnlyFixture({ adminHasWorkspace: false });
  const globalWorkspace = await runProductionLiveQaCli({
    argv: ["--read-only"],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_SUB2API_ADMIN_EMAIL: ADMIN_EMAIL,
      OPL_SUB2API_ADMIN_PASSWORD: ADMIN_PASSWORD
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: globalWorkspaceFixture.fetchImpl,
    browserFactory: globalWorkspaceFixture.browserFactory
  });
  assert.equal(globalWorkspace, 0, stderr);
  assert.equal(JSON.parse(stdout).checks.fabric, "available");

  stderr = "";
  let calls = 0;
  const denied = await runProductionLiveQaCli({
    argv: ["--allow-gateway-write", "--allow-model-write", "--approval-id", "approval-production-verification"],
    env: {},
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(denied, 1);
  assert.match(stderr, /production_live_qa_approval_manifest_required/);
  assert.equal(calls, 0);
});
