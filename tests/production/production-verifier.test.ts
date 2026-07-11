import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { basename } from "node:path";
import test from "node:test";

import {
  runProductionVerifierCli,
  verifyProductionChain,
  verifyWorkspaceBrowserUi
} from "../../tools/production-verifier.ts";

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

function binaryResponse(body, headers = {}) {
  return {
    status: 200,
    ok: true,
    headers: new Headers({ "content-type": "application/octet-stream", ...headers }),
    json: async () => JSON.parse(body),
    text: async () => body
  };
}

function capturedBody(body) {
  if (!body || typeof body !== "string") return body || null;
  return JSON.parse(body);
}

function redirectResponse(location, setCookie) {
  return {
    status: 302,
    ok: false,
    headers: new Headers({ location, "set-cookie": setCookie }),
    json: async () => ({}),
    text: async () => ""
  };
}

function tkeChain({ workspaceUrl = "https://workspace.medopl.cn/w/ws-tke-prod001/?token=share_tke_prod" } = {}) {
  const compute = {
    id: "compute-prod001",
    ownerAccountId: "pi-prod",
    packageId: "basic",
    provider: "tencent-tke",
    providerResourceId: "node/opl-node-prod001",
    instanceId: "ins-prod001",
    nodeName: "opl-node-prod001",
    privateIp: "10.0.0.21",
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
    access: { tokenStatus: "active", credentialStatus: "configured", requiresLogin: false, account: "admin", password: "runtime-password-from-console" }
  };
  const replacementCompute = {
    ...compute,
    id: "compute-prod002",
    providerResourceId: "node/opl-node-prod002",
    instanceId: "ins-prod002",
    nodeName: "opl-node-prod002",
    privateIp: "10.0.0.22",
    runtime: { service: "service/opl-compute-prod002", serviceName: "opl-compute-prod002" }
  };
  const replacementAttachment = {
    ...attachment,
    id: "attach-prod002",
    computeAllocationId: replacementCompute.id,
    providerAttachmentId: "deployment/opl-compute-prod002:pvc/opl-storage-prod001-data:/data"
  };
  const replacementWorkspace = {
    ...workspace,
    computeAllocationId: replacementCompute.id,
    attachmentId: replacementAttachment.id,
    server: { ...workspace.server, id: replacementCompute.providerResourceId },
    docker: { ...workspace.docker, id: replacementCompute.providerResourceId, service: replacementCompute.runtime.service }
  };
  return { compute, storage, attachment, workspace, replacementCompute, replacementAttachment, replacementWorkspace };
}

function readyRuntimeStatus(workspace) {
  return {
    provider: "tencent-tke",
    workspaceId: workspace.id,
    runtimeId: "op-runtime-prod002",
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
  const persistenceText = "opl persistence prod-run";
  const transferText = `${"x".repeat(4 << 20)}opl transfer prod-run`;
  const transferDigest = createHash("sha256").update(transferText).digest("hex");
  const transfer = {
    transferId: "transfer-prod-run",
    organizationId: "org-production-verifier",
    workspaceId: chain.workspace.id,
    projectId: "project-prod-run",
    path: "production-verifier/opl-transfer-prod-run.txt",
    digest: transferDigest,
    size: Buffer.byteLength(transferText),
    chunkSize: 4 << 20,
    chunkCount: 2,
    receivedChunks: [],
    status: "uploading"
  };
  const pricingVersion = "opl-tencent-v1";
  const computePriceSnapshot = { packageId: "basic", unitPriceCents: 100, currency: "CNY" };
  const storagePriceSnapshot = { packageId: "basic", unitPriceCents: 100, currency: "CNY" };
  const costTags = {
    opl_account_id: "pi-prod",
    opl_workspace_id: chain.workspace.id,
    opl_resource_id: chain.replacementCompute.id,
    opl_operation_id: "op-runtime-prod002"
  };
  return {
    "GET /api/production/readiness": { ready: true, missingEnv: [], missingTools: [], failedChecks: [], checks: [] },
    "GET /api/runtime/readiness": { provider: "tencent-tke", ready: true, missingEnv: [], missingTools: [] },
    "POST /api/billing/topups": { id: "pi-prod", balance: 1000, frozen: 0 },
    "POST /api/compute-allocations": chain.compute,
    "POST /api/compute-allocations#2": chain.replacementCompute,
    [`GET /api/compute-allocations/${chain.compute.id}?accountId=pi-prod`]: chain.compute,
    [`GET /api/compute-allocations/${chain.replacementCompute.id}?accountId=pi-prod`]: chain.replacementCompute,
    "POST /api/storage-volumes": chain.storage,
    "POST /api/storage-attachments": chain.attachment,
    "POST /api/storage-attachments#2": chain.replacementAttachment,
    "POST /api/workspaces": chain.workspace,
    "POST /api/workspaces#2": chain.replacementWorkspace,
    "POST /api/workspaces/runtime-status": readyRuntimeStatus(chain.workspace),
    "POST /api/workspaces/runtime-status#2": readyRuntimeStatus(chain.replacementWorkspace),
    [`GET ${chain.workspace.url}`]: "<html>one-person-lab-app</html>",
    [`GET ${workspaceUrl(chain.workspace.url, "/api/auth/user")}`]: {
      success: true,
      user: { id: "opl-webui-admin", username: "admin" }
    },
    [`POST ${workspaceUrl(chain.workspace.url, "/api/fs/write")}`]: { success: true, data: true },
    [`POST ${workspaceUrl(chain.workspace.url, "/api/fs/read")}`]: { success: true, data: persistenceText },
    "POST /api/projects": { projectId: transfer.projectId, organizationId: transfer.organizationId, workspaceId: transfer.workspaceId },
    [`POST /api/workspaces/${chain.workspace.id}/transfers`]: transfer,
    [`PUT /api/workspaces/${chain.workspace.id}/transfers/${transfer.transferId}/chunks/0`]: { ...transfer, receivedChunks: [0] },
    [`GET /api/workspaces/${chain.workspace.id}/transfers/${transfer.transferId}`]: { ...transfer, receivedChunks: [0] },
    [`PUT /api/workspaces/${chain.workspace.id}/transfers/${transfer.transferId}/chunks/1`]: { ...transfer, receivedChunks: [0, 1] },
    [`POST /api/workspaces/${chain.workspace.id}/transfers/${transfer.transferId}/complete`]: { ...transfer, receivedChunks: [0, 1], status: "completed" },
    [`GET /api/workspaces/${chain.workspace.id}/contents/${transferDigest}`]: { binaryBody: transferText, digest: transferDigest, path: transfer.path },
    [`GET ${chain.workspace.url}#2`]: "<html>one-person-lab-app</html>",
    [`POST ${workspaceUrl(chain.workspace.url, "/api/fs/read")}#2`]: { success: true, data: persistenceText },
    "POST /api/billing/resource-settlements": { entries: [
      { accountId: "pi-prod", computeAllocationId: chain.replacementCompute.id, type: "compute_debit", pricingVersion, priceSnapshot: computePriceSnapshot, providerCostEvidenceRef: "fabric:op-runtime-prod002", quantity: 1, unit: "verification" }
    ] },
    "POST /api/billing/resource-settlements#2": { entries: [
      { accountId: "pi-prod", storageId: chain.storage.id, type: "storage_debit", pricingVersion, priceSnapshot: storagePriceSnapshot, providerCostEvidenceRef: "fabric:op-runtime-prod002", quantity: 1, unit: "verification" }
    ] },
    "GET /api/state?accountId=pi-prod": {
      wallet: { accountId: "pi-prod", balance: 999, frozen: 10, available: 989, totalSpentCents: 200 },
      billingLedger: [
        { id: "ledger-compute", accountId: "pi-prod", computeAllocationId: chain.compute.id, type: "compute_hold" },
        { id: "ledger-storage", accountId: "pi-prod", storageId: chain.storage.id, type: "storage_hold" },
        { id: "ledger-compute-debit", accountId: "pi-prod", computeAllocationId: chain.replacementCompute.id, type: "compute_debit", pricingVersion, priceSnapshot: computePriceSnapshot, providerCostEvidenceRef: "fabric:op-runtime-prod002", quantity: 1, unit: "verification" },
        { id: "ledger-storage-debit", accountId: "pi-prod", storageId: chain.storage.id, type: "storage_debit", pricingVersion, priceSnapshot: storagePriceSnapshot, providerCostEvidenceRef: "fabric:op-runtime-prod002", quantity: 1, unit: "verification" }
      ],
      walletTransactions: [
        { id: "wallet-compute-debit", accountId: "pi-prod", metadata: { computeAllocationId: chain.replacementCompute.id }, type: "compute_debit", balanceCents: 900, frozenCents: 10, availableCents: 890, totalSpentCents: 100 },
        { id: "wallet-storage-debit", accountId: "pi-prod", metadata: { storageId: chain.storage.id }, type: "storage_debit", balanceCents: 800, frozenCents: 10, availableCents: 790, totalSpentCents: 200 }
      ],
      resourceLedgerEvidence: [
        { accountId: "pi-prod", workspaceId: chain.replacementWorkspace.id, computeAllocationId: chain.replacementCompute.id, storageId: chain.storage.id, attachmentId: chain.replacementAttachment.id, operationId: "op-runtime-prod002", costTags, ledgerEntryIds: ["ledger-compute-debit", "ledger-storage-debit"], walletTransactionIds: ["wallet-compute-debit", "wallet-storage-debit"] }
      ],
      runtimeOperations: [
        { operationId: "op-runtime-prod002", resourceKind: "runtime", resourceId: chain.replacementWorkspace.id, workspaceId: chain.replacementWorkspace.id, status: "succeeded", providerRequestId: "req-runtime-prod002", costTags }
      ]
    },
    "POST /api/storage-attachments/detach": { ...chain.attachment, status: "detached" },
    "POST /api/storage-attachments/detach#2": { ...chain.replacementAttachment, status: "detached" },
    [`POST /api/compute-allocations/${chain.compute.id}/destroy`]: { ...chain.compute, status: "destroyed", billingStatus: "stopped" },
    [`POST /api/compute-allocations/${chain.replacementCompute.id}/destroy`]: { ...chain.replacementCompute, status: "destroyed", billingStatus: "stopped" },
    "POST /api/storage-volumes/destroy": { ...chain.storage, status: "destroyed", billingStatus: "stopped" }
  };
}

function keyedFetch({ responses, requests = [], responseHeaders = null, statusByKey = {}, consoleOrigin = "https://console.oplcloud.cn" }) {
  const requestCounts = new Map();
  return async (url, options = {}) => {
    const parsed = new URL(String(url));
    const method = options.method || "GET";
    let key = parsed.origin === consoleOrigin ? `${method} ${parsed.pathname}${parsed.search}` : `${method} ${String(url)}`;
    if (parsed.origin !== consoleOrigin && method === "GET" && parsed.searchParams.has("token") && options.redirect === "manual") {
      const count = (requestCounts.get(key) || 0) + 1;
      requestCounts.set(key, count);
      key = count === 1 ? key : `${key}#${count}`;
      requests.push({
        key,
        cookie: options.headers?.cookie || "",
        csrf: options.headers?.["x-opl-csrf"] || "",
        operatorToken: options.headers?.["x-opl-operator-token"] || "",
        idempotencyKey: options.headers?.["Idempotency-Key"] || "",
        body: capturedBody(options.body),
        redirect: options.redirect || ""
      });
      const workspaceId = parsed.pathname.split("/").filter(Boolean).pop() || "workspace";
      const token = parsed.searchParams.get("token") || "";
      const clean = new URL(String(url));
      clean.searchParams.delete("token");
      return redirectResponse(`${clean.pathname}${clean.search}`, `opl_ws_active=${workspaceId}; Path=/; HttpOnly, opl_ws_${workspaceId}=${token}; Path=/; HttpOnly`);
    }
    if (
      parsed.origin !== consoleOrigin ||
      [
        "POST /api/compute-allocations",
        "POST /api/storage-attachments",
        "POST /api/workspaces",
        "POST /api/workspaces/runtime-status",
        "POST /api/billing/resource-settlements",
        "POST /api/storage-attachments/detach"
      ].includes(key) ||
      key.startsWith("GET /api/compute-allocations/")
    ) {
      const count = (requestCounts.get(key) || 0) + 1;
      requestCounts.set(key, count);
      key = count === 1 ? key : `${key}#${count}`;
    }
    requests.push({
      key,
      cookie: options.headers?.cookie || "",
      csrf: options.headers?.["x-opl-csrf"] || "",
      operatorToken: options.headers?.["x-opl-operator-token"] || "",
      idempotencyKey: options.headers?.["Idempotency-Key"] || "",
      contentType: options.headers?.["content-type"] || options.headers?.["Content-Type"] || "",
      body: capturedBody(options.body)
    });
    let responseKey = key;
    if (parsed.origin !== consoleOrigin && method === "GET" && parsed.pathname.startsWith("/w/") && !parsed.searchParams.has("token")) {
      const cookies = Object.fromEntries(String(options.headers?.cookie || "").split(";").map((entry) => {
        const [name, ...value] = entry.trim().split("=");
        return [name, value.join("=")];
      }).filter(([name]) => name));
      const token = cookies[`opl_ws_${cookies.opl_ws_active}`] || "";
      if (token) {
        const lookup = new URL(String(url));
        lookup.searchParams.set("token", token);
        responseKey = `${method} ${lookup.toString()}${key.match(/#\d+$/)?.[0] || ""}`;
      }
    }
    const payload = responses[responseKey] ?? responses[responseKey.replace(/#\d+$/, "")] ?? responses[key] ?? responses[key.replace(/#\d+$/, "")];
    if (typeof payload === "string") return htmlResponse(payload);
    if (payload) {
      if (typeof payload.binaryBody === "string") {
        return binaryResponse(payload.binaryBody, {
          "x-content-sha256": payload.digest,
          "x-workspace-path": payload.path
        });
      }
      if (key === "POST /api/auth/operator-login" && responseHeaders) return jsonResponse(payload, 200, responseHeaders);
      if (String(key).includes("/api/auth/user")) {
        return jsonResponse(payload, 200, new Headers({
          "content-type": "application/json",
          "set-cookie": "aionui-session=api-session; Path=/; HttpOnly"
        }));
      }
      return jsonResponse(payload, statusByKey[key] || statusByKey[key.replace(/#\d+$/, "")] || 200);
    }
    throw new Error(`unexpected_request:${key}`);
  };
}

function workspaceCookieGatewayFetch({ responses, requests = [] }) {
  const consoleFetch = keyedFetch({ responses, requests });
  const requestCounts = new Map();
  const workspaceId = "ws-tke-prod001";
  const token = "share_tke_prod";
  const cookie = `opl_ws_active=${workspaceId}; opl_ws_${workspaceId}=${token}`;
  const setCookie = `opl_ws_active=${workspaceId}; Path=/; HttpOnly, opl_ws_${workspaceId}=${token}; Path=/; HttpOnly`;
  return async (url, options = {}) => {
    const parsed = new URL(String(url));
    if (parsed.origin !== "https://workspace.medopl.cn") return consoleFetch(url, options);

    const method = options.method || "GET";
    const requestCookie = options.headers?.cookie || "";
    if (method === "GET" && parsed.searchParams.get("token") === token) {
      requests.push({ key: `${method} ${String(url)}`, cookie: requestCookie, redirect: options.redirect || "" });
      const clean = new URL(String(url));
      clean.searchParams.delete("token");
      return redirectResponse(`${clean.pathname}${clean.search}`, setCookie);
    }

    if (!requestCookie.includes(`opl_ws_active=${workspaceId}`) || !requestCookie.includes(`opl_ws_${workspaceId}=${token}`)) {
      return htmlResponse("<!doctype html><p>OPL Workspace 访问令牌无效。</p>", 403);
    }

    const keyUrl = new URL(String(url));
    if (method === "GET" && keyUrl.pathname.startsWith("/w/") && !keyUrl.searchParams.has("token")) {
      keyUrl.searchParams.set("token", token);
    }
    let key = `${method} ${keyUrl.toString()}`;
    const count = (requestCounts.get(key) || 0) + 1;
    requestCounts.set(key, count);
    key = count === 1 ? key : `${key}#${count}`;
    requests.push({ key, cookie: requestCookie, redirect: options.redirect || "" });
    const payload = responses[key] ?? responses[key.replace(/#\d+$/, "")];
    if (typeof payload === "string") return htmlResponse(payload);
    if (payload && String(key).includes("/api/auth/user")) {
      return jsonResponse(payload, 200, new Headers({
        "content-type": "application/json",
        "set-cookie": "aionui-session=api-session; Path=/; HttpOnly"
      }));
    }
    if (payload) return jsonResponse(payload);
    throw new Error(`unexpected_request:${key}`);
  };
}

function workspaceUrl(baseUrl, path) {
  const parsed = new URL(baseUrl);
  parsed.pathname = `/${path.replace(/^\//, "")}`;
  parsed.search = "";
  parsed.hash = "";
  return parsed.toString();
}

function scrubbedWorkspaceUrl(baseUrl) {
  const parsed = new URL(baseUrl);
  parsed.searchParams.delete("token");
  return parsed.toString();
}

function fakeBrowserFactory(actions = [], { failWaitAt = 0 } = {}) {
  let waitCount = 0;
  const page = {
    async goto(url) {
      actions.push(["goto", url]);
    },
    locator(selector) {
      actions.push(["locator", selector]);
      return {
        first() {
          return this;
        },
        async count() {
          return selector === 'input[type="file"]' ? 1 : 0;
        },
        async setInputFiles(filePath) {
          actions.push(["setInputFiles", filePath]);
        }
      };
    },
    getByRole(role, options = {}) {
      const roleName = String(options.name || "");
      actions.push(["getByRole", role, roleName]);
      return {
        first() {
          return this;
        },
        async fill(value) {
          actions.push(["fill", role, value]);
        },
        async click() {
          actions.push(["click", role, roleName]);
        }
      };
    },
    async waitForFunction(_fn, ...args) {
      waitCount += 1;
      actions.push(["waitForFunction", ...args.slice(0, 2)]);
      if (failWaitAt === waitCount) throw new Error("Timeout 180000ms exceeded.");
      return true;
    },
    async screenshot(options = {}) {
      actions.push(["screenshot", options.path || ""]);
    }
  };
  return {
    chromium: {
      async launch() {
        actions.push(["launch"]);
        return {
          async newContext() {
            actions.push(["newContext"]);
            return {
              async addCookies(cookies) {
                actions.push(["addCookies", cookies]);
              },
              async newPage() {
                actions.push(["newPage"]);
                return page;
              }
            };
          },
          async newPage() {
            actions.push(["newPage"]);
            return page;
          },
          async close() {
            actions.push(["close"]);
          }
        };
      }
    }
  };
}

function fakeWorkspaceBrowserFactory(actions = [], {
  assistantSelectionTakesEffect = true,
  firstTextboxIsNotComposer = false,
  assistantLabels = ["@Research", "@Grants", "@PPT"],
  domAssistantSelection = false,
  assistantReplies = false,
  roleAssistantSelection = true
} = {}) {
  const state = {
    bodyText: `Select an assistant to start a task\n${assistantLabels.join("\n")}`,
    prompt: ""
  };
  const page = {
    async goto(url) {
      actions.push(["goto", url]);
    },
    locator(selector) {
      actions.push(["locator", selector]);
      return {
        first() {
          return this;
        },
        async count() {
          return selector === 'input[type="file"]' ? 1 : 0;
        },
        async setInputFiles(filePath) {
          actions.push(["setInputFiles", filePath]);
          state.bodyText += `\n${basename(filePath)}`;
        }
      };
    },
    getByRole(role, options = {}) {
      const roleName = String(options.name || "");
      actions.push(["getByRole", role, roleName]);
      return {
        first() {
          this.target = "first";
          return this;
        },
        last() {
          this.target = "last";
          return this;
        },
        async fill(value) {
          const target = this.target || "first";
          actions.push(["fill", role, value, target]);
          if (!(role === "textbox" && firstTextboxIsNotComposer && target === "first")) {
            state.prompt = value;
          }
        },
        async click() {
          actions.push(["click", role, roleName]);
          if (roleAssistantSelection && /Research/i.test(roleName) && assistantSelectionTakesEffect) {
            state.bodyText = state.bodyText.replace("Select an assistant to start a task", "Research assistant selected");
          }
          if (/发送|Send|提交|运行|Ask/i.test(roleName) && !/Select an assistant to start a task/i.test(state.bodyText)) {
            state.bodyText += `\n${state.prompt}`;
            if (assistantReplies) state.bodyText += `\nassistant reply: ${state.prompt.replace("请只回复：", "")}`;
          }
        }
      };
    },
    async waitForFunction(fn, arg, options = {}) {
      actions.push(["waitForFunction", arg, options]);
      const previousDocument = globalThis.document;
      const previousWindow = globalThis.window;
      const visiblePromptElement = {
        value: state.prompt,
        textContent: state.prompt,
        innerText: state.prompt,
        getBoundingClientRect: () => ({ width: state.prompt ? 360 : 0, height: state.prompt ? 40 : 0 })
      };
      globalThis.document = {
        body: { innerText: state.bodyText },
        querySelectorAll: () => (state.prompt ? [visiblePromptElement] : [])
      };
      globalThis.window = {
        getComputedStyle: () => ({ visibility: "visible", display: "block" })
      };
      try {
        if (!fn(arg)) throw new Error("Timeout exceeded.");
      } finally {
        globalThis.document = previousDocument;
        globalThis.window = previousWindow;
      }
    },
    async screenshot(options = {}) {
      actions.push(["screenshot", options.path || ""]);
    }
  };
  if (domAssistantSelection) {
    page.evaluate = async (fn, arg) => {
      actions.push(["evaluate", arg || null]);
      const previousDocument = globalThis.document;
      const previousWindow = globalThis.window;
      const elements = assistantLabels.map((label, index) => ({
        textContent: label,
        innerText: label,
        getBoundingClientRect: () => ({ width: 220, height: 78, top: 450 + index * 88, bottom: 528 + index * 88, left: 390, right: 610 }),
        getAttribute: () => "",
        click() {
          actions.push(["domAssistantClick", label]);
          if (assistantSelectionTakesEffect) state.bodyText = state.bodyText.replace("Select an assistant to start a task", `${label} selected`);
        }
      }));
      globalThis.document = {
        body: { innerText: state.bodyText },
        querySelector: () => null,
        querySelectorAll: () => elements
      };
      globalThis.window = {
        getComputedStyle: () => ({ visibility: "visible", display: "block" })
      };
      try {
        return fn(arg);
      } finally {
        globalThis.document = previousDocument;
        globalThis.window = previousWindow;
      }
    };
  }
  return {
    chromium: {
      async launch() {
        actions.push(["launch"]);
        return {
          async newPage() {
            actions.push(["newPage"]);
            return page;
          },
          async close() {
            actions.push(["close"]);
          }
        };
      }
    }
  };
}

function fakeAionUiLoginBrowserFactory(actions = []) {
  const state = { loggedIn: false, username: "", password: "" };
  const page = {
    async goto(url) {
      actions.push(["goto", url]);
    },
    locator(selector) {
      actions.push(["locator", selector]);
      return {
        first() {
          return this;
        },
        last() {
          return this;
        },
        async count() {
          if (selector.includes('input[type="file"]')) return state.loggedIn ? 1 : 0;
          if (/username|autocomplete="username"/i.test(selector)) return state.loggedIn ? 0 : 1;
          if (/password|autocomplete="current-password"/i.test(selector)) return state.loggedIn ? 0 : 1;
          return 0;
        },
        async fill(value) {
          actions.push(["fillLocator", selector, value]);
          if (/username|autocomplete="username"/i.test(selector)) state.username = value;
          if (/password|autocomplete="current-password"/i.test(selector)) state.password = value;
        },
        async setInputFiles(filePath) {
          actions.push(["setInputFiles", filePath]);
        }
      };
    },
    getByRole(role, options = {}) {
      const roleName = String(options.name || "");
      actions.push(["getByRole", role, roleName]);
      return {
        first() {
          return this;
        },
        last() {
          return this;
        },
        async fill(value) {
          actions.push(["fill", role, value]);
        },
        async click() {
          actions.push(["click", role, roleName]);
          if (/Sign In|登录|登入/i.test(roleName) && state.username === "admin" && state.password === "ManagedWebuiPass2026!") {
            state.loggedIn = true;
          }
        }
      };
    },
    async waitForFunction(fn, arg, options = {}) {
      actions.push(["waitForFunction", arg, options]);
      return fn ? true : true;
    },
    async screenshot(options = {}) {
      actions.push(["screenshot", options.path || ""]);
    }
  };
  return {
    chromium: {
      async launch() {
        actions.push(["launch"]);
        return {
          async newContext() {
            actions.push(["newContext"]);
            return {
              async addCookies(cookies) {
                actions.push(["addCookies", cookies]);
              },
              async newPage() {
                actions.push(["newPage"]);
                return page;
              }
            };
          },
          async newPage() {
            actions.push(["newPage"]);
            return page;
          },
          async close() {
            actions.push(["close"]);
          }
        };
      }
    }
  };
}

function fakeGuidDomBrowserFactory(actions = [], { firstRun = false, setupButtonText = "Finish setup" } = {}) {
  const state = {
    setup: firstRun,
    hash: "#/home",
    bodyText: "Select an assistant to start a task\n@MAS\n@Research",
    accessKey: "",
    prompt: "",
    fileName: "",
    selected: false
  };
  class FakeTextArea {
    get value() {
      return state.prompt;
    }
    set value(next) {
      state.prompt = String(next || "");
    }
    dispatchEvent(event) {
      actions.push(["dispatchEvent", event.type]);
      return true;
    }
    getBoundingClientRect() {
      return { width: 360, height: 48, top: 300, bottom: 348, left: 32, right: 392 };
    }
  }
  const textarea = new FakeTextArea();
  const accessInput = {
    type: "password",
    placeholder: "Enter access key",
    get value() {
      return state.accessKey;
    },
    set value(next) {
      state.accessKey = String(next || "");
    },
    dispatchEvent(event) {
      actions.push(["accessDispatchEvent", event.type]);
      return true;
    },
    getAttribute(name) {
      if (name === "placeholder") return this.placeholder;
      return "";
    },
    closest() {
      return { innerText: "Model Access Enter access key" };
    },
    getBoundingClientRect() {
      return { width: 320, height: 40, top: 220, bottom: 260, left: 32, right: 352 };
    }
  };
  const finishButton = {
    disabled: false,
    innerText: setupButtonText,
    click() {
      actions.push(["domClick", "finish-setup"]);
      if (state.accessKey) state.setup = false;
    },
    getAttribute() {
      return "";
    },
    getBoundingClientRect() {
      return { width: 140, height: 40, top: 280, bottom: 320, left: 32, right: 172 };
    }
  };
  const card = {
    textContent: "@MAS",
    click() {
      actions.push(["domClick", "preset-pill-mas"]);
      state.selected = true;
      state.bodyText = state.bodyText.replace("Select an assistant to start a task", "MAS selected");
    },
    getBoundingClientRect() {
      return { width: 120, height: 36, top: 120, bottom: 156, left: 32, right: 152 };
    }
  };
  const sendButton = {
    get disabled() {
      return !state.selected || !state.prompt;
    },
    getAttribute(name) {
      if (name === "disabled" && this.disabled) return "";
      if (name === "data-testid") return "guid-send-btn";
      return null;
    },
    click() {
      actions.push(["domClick", "guid-send-btn"]);
      if (this.disabled) return;
      state.bodyText += `\n${state.prompt}\nassistant:${state.prompt.match(/OPL_BROWSER_E2E_[\w-]+/)?.[0] || "ok"}`;
    },
    getBoundingClientRect() {
      return { width: 36, height: 36, top: 360, bottom: 396, left: 660, right: 696 };
    }
  };
  const visibleStyle = { visibility: "visible", display: "block" };
  function installDom() {
    const previous = {
      document: globalThis.document,
      window: globalThis.window,
      Event: globalThis.Event
    };
    globalThis.Event = class {
      constructor(type) {
        this.type = type;
        this.bubbles = true;
      }
    };
    globalThis.window = {
      location: {
        get hash() {
          return state.hash;
        },
        set hash(next) {
          actions.push(["setHash", next]);
          state.hash = String(next);
        }
      },
      HTMLTextAreaElement: FakeTextArea,
      HTMLInputElement: class {},
      getComputedStyle: () => visibleStyle
    };
    globalThis.document = {
      body: {
        get innerText() {
          const screen = state.setup
            ? "Prepare One Person Lab\nWorkspace root Ready\nLocal assistant Ready\nModel Access Unknown\nEnter access key\nFinish setup"
            : state.bodyText;
          return [screen, state.fileName].filter(Boolean).join("\n");
        }
      },
      querySelector(selector) {
        actions.push(["querySelector", selector]);
        if (state.setup) return null;
        if (selector.includes('input[type="file"]')) return { getBoundingClientRect: () => ({ width: 1, height: 1 }) };
        if (selector.includes("preset-pill-mas")) return card;
        if (selector.includes("guid-input")) return textarea;
        if (selector.includes("guid-send-btn")) return sendButton;
        return null;
      },
      querySelectorAll(selector) {
        actions.push(["querySelectorAll", selector]);
        if (state.setup) {
          if (selector.includes("input") || selector.includes("textarea")) return [accessInput];
          if (selector.includes("button")) return [finishButton];
          return [];
        }
        if (selector.includes("textarea") || selector.includes("guid-input")) return [textarea];
        if (selector.includes("button") || selector.includes("guid-send-btn")) return [sendButton];
        return [];
      }
    };
    return () => {
      globalThis.document = previous.document;
      globalThis.window = previous.window;
      globalThis.Event = previous.Event;
    };
  }
  const page = {
    async goto(url) {
      actions.push(["goto", url]);
    },
    locator(selector) {
      actions.push(["locator", selector]);
      return {
        first() {
          return this;
        },
        last() {
          this.target = "last";
          return this;
        },
        async count() {
          return selector === 'input[type="file"]' && !state.setup ? 1 : 0;
        },
        async fill(value) {
          actions.push(["fillLocator", selector, value, this.target || "first"]);
          if (state.setup) state.accessKey = value;
        },
        async setInputFiles(filePath) {
          actions.push(["setInputFiles", filePath]);
          state.fileName = basename(filePath);
        }
      };
    },
    getByRole(role, options = {}) {
      actions.push(["getByRole", role, String(options.name || "")]);
      return {
        first() {
          return this;
        },
        last() {
          return this;
        },
        async fill(value) {
          actions.push(["roleFill", role, value]);
          throw new Error("generic role textbox is not the active guid composer");
        },
        async click() {
          actions.push(["roleClick", role, String(options.name || "")]);
          const name = options.name;
          const matchesName = name instanceof RegExp ? name.test(setupButtonText) : String(name || "") === setupButtonText;
          if (state.setup && matchesName) {
            if (state.accessKey) state.setup = false;
            return;
          }
          throw new Error("guid page controls are data-testid only in this fixture");
        }
      };
    },
    getByText(text) {
      actions.push(["getByText", String(text)]);
      return {
        first() {
          return this;
        },
        async click() {
          throw new Error("text click does not select the guid assistant in this fixture");
        }
      };
    },
    async evaluate(fn, arg) {
      actions.push(["evaluate"]);
      const restore = installDom();
      try {
        return fn(arg);
      } finally {
        restore();
      }
    },
    async waitForFunction(fn, arg, options = {}) {
      actions.push(["waitForFunction", arg, options]);
      const restore = installDom();
      try {
        const result = fn(arg);
        if (!result) throw new Error("Timeout exceeded.");
        return result;
      } finally {
        restore();
      }
    },
    async screenshot(options = {}) {
      actions.push(["screenshot", options.path || ""]);
    }
  };
  return {
    chromium: {
      async launch() {
        actions.push(["launch"]);
        return {
          async newPage() {
            actions.push(["newPage"]);
            return page;
          },
          async close() {
            actions.push(["close"]);
          }
        };
      }
    }
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

test("staging-local verifier can use a local Console origin while still requiring public Workspace URLs", async () => {
  const requests = [];
  const chain = tkeChain();
  const result = await verifyProductionChain({
    origin: "http://127.0.0.1:8787",
    allowPrivateConsoleOrigin: true,
    accountId: "pi-prod",
    workspaceName: "Local To Staging Verification Lab",
    runId: "prod-run",
    fetchImpl: keyedFetch({
      responses: chainResponses(chain),
      requests,
      consoleOrigin: "http://127.0.0.1:8787"
    })
  });

  assert.equal(requests[0].key, "GET /api/production/readiness");
  assert.equal(result.ok, true);

  await assert.rejects(
    verifyProductionChain({
      origin: "http://127.0.0.1:8787",
      allowPrivateConsoleOrigin: true,
      accountId: "pi-prod",
      workspaceName: "Bad Workspace URL",
      fetchImpl: keyedFetch({
        responses: chainResponses(tkeChain({ workspaceUrl: "http://127.0.0.1:3000/" })),
        requests: [],
        consoleOrigin: "http://127.0.0.1:8787"
      })
    }),
    /public_workspace_url_required/
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
    runId: "prod-run",
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
    `GET ${scrubbedWorkspaceUrl(chain.workspace.url)}`,
    `GET ${workspaceUrl(chain.workspace.url, "/api/auth/user")}`,
    `GET ${workspaceUrl(chain.workspace.url, "/api/auth/user")}#2`,
    `POST ${workspaceUrl(chain.workspace.url, "/api/fs/write")}`,
    `POST ${workspaceUrl(chain.workspace.url, "/api/fs/read")}`,
    "POST /api/projects",
    `POST /api/workspaces/${chain.workspace.id}/transfers`,
    `PUT /api/workspaces/${chain.workspace.id}/transfers/transfer-prod-run/chunks/0`,
    `GET /api/workspaces/${chain.workspace.id}/transfers/transfer-prod-run`,
    `PUT /api/workspaces/${chain.workspace.id}/transfers/transfer-prod-run/chunks/1`,
    `POST /api/workspaces/${chain.workspace.id}/transfers/transfer-prod-run/complete`,
    `GET /api/workspaces/${chain.workspace.id}/contents/${createHash("sha256").update(`${"x".repeat(4 << 20)}opl transfer prod-run`).digest("hex")}`,
    "POST /api/storage-attachments/detach",
    `POST /api/compute-allocations/${chain.compute.id}/destroy`,
    "POST /api/compute-allocations#2",
    "POST /api/storage-attachments#2",
    "POST /api/workspaces#2",
    "POST /api/workspaces/runtime-status#2",
    `GET ${chain.workspace.url}#2`,
    `GET ${scrubbedWorkspaceUrl(chain.workspace.url)}#2`,
    `GET ${workspaceUrl(chain.workspace.url, "/api/auth/user")}#3`,
    `POST ${workspaceUrl(chain.workspace.url, "/api/fs/read")}#2`,
    "POST /api/billing/resource-settlements",
    "POST /api/billing/resource-settlements#2",
    "GET /api/state?accountId=pi-prod",
    "POST /api/storage-attachments/detach#2",
    `POST /api/compute-allocations/${chain.replacementCompute.id}/destroy`,
    "POST /api/storage-volumes/destroy"
  ]);
  assert.equal(requests.find((request) => request.key === "POST /api/billing/topups").idempotencyKey, "production_verification_credit:prod-run");
  assert.deepEqual(requests.find((request) => request.key === "POST /api/workspaces").body, {
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    attachmentId: chain.attachment.id
  });
  assert.equal(requests.find((request) => request.key === "POST /api/storage-attachments").body.computeAllocationId, chain.compute.id);
  assert.equal(requests.find((request) => request.key === "POST /api/storage-attachments").body.storageId, chain.storage.id);
  assert.deepEqual(requests.find((request) => request.key === `POST ${workspaceUrl(chain.workspace.url, "/api/fs/write")}`).body, {
    path: "/data/opl-e2e-prod-run.txt",
    data: "opl persistence prod-run"
  });
  assert.deepEqual(requests.find((request) => request.key === `POST ${workspaceUrl(chain.workspace.url, "/api/fs/read")}`).body, {
    path: "/data/opl-e2e-prod-run.txt",
    workspace: "/data"
  });
  assert.ok(requests.filter((request) => request.key.includes("/chunks/")).every((request) => request.contentType === "application/octet-stream"));
  assert.equal(requests.find((request) => request.key === "POST /api/storage-attachments#2").body.storageId, chain.storage.id);
  assert.deepEqual(requests.find((request) => request.key === "POST /api/workspaces#2").body, {
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    attachmentId: chain.replacementAttachment.id
  });
  assert.equal(chain.replacementWorkspace.id, chain.workspace.id);
  assert.equal(chain.replacementWorkspace.url, chain.workspace.url);
  assert.deepEqual(requests.find((request) => request.key === `POST ${workspaceUrl(chain.workspace.url, "/api/fs/read")}#2`).body, {
    path: "/data/opl-e2e-prod-run.txt",
    workspace: "/data"
  });
  assert.ok(!requests.some((request) => request.key.includes("/login")));
  assert.ok(requests.some((request) =>
    request.key === `GET ${workspaceUrl(chain.workspace.url, "/api/auth/user")}` &&
    request.cookie.includes("opl_ws_active=ws-tke-prod001")
  ));
  const computeSettlementBody = requests.find((request) => request.key === "POST /api/billing/resource-settlements").body;
  const storageSettlementBody = requests.find((request) => request.key === "POST /api/billing/resource-settlements#2").body;
  assert.equal(computeSettlementBody.pricingVersion, "opl-tencent-v1");
  assert.equal(computeSettlementBody.providerCostEvidenceRef, "fabric:op-runtime-prod002");
  assert.deepEqual(computeSettlementBody.priceSnapshot, { packageId: "basic", resourceType: "compute", unitPriceCents: 100, currency: "CNY", source: "production_verifier" });
  assert.equal(storageSettlementBody.pricingVersion, "opl-tencent-v1");
  assert.equal(storageSettlementBody.providerCostEvidenceRef, "fabric:op-runtime-prod002");
  assert.deepEqual(storageSettlementBody.priceSnapshot, { packageId: "basic", resourceType: "storage", unitPriceCents: 100, currency: "CNY", source: "production_verifier" });
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
    "workspace_url_token_scrubbed:true",
    "workspace_runtime_auth:true",
    "workspace_file_written:true",
    "workspace_file_read:true",
    "workspace_content_transfer_interrupted:true",
    "workspace_content_transfer_completed:true",
    "workspace_content_transfer_downloaded:true",
    "verification_storage_detached:true",
    "verification_compute_destroyed:true",
    "replacement_compute_created:true",
    "replacement_storage_attached:true",
    "replacement_workspace_created:true",
    "replacement_workspace_runtime_status:true",
    "replacement_workspace_url:true",
    "replacement_workspace_url_token_scrubbed:true",
    "workspace_persisted_file_read:true",
    "resource_billing_settled:true",
    "ledger_and_wallet_transactions_verified:true",
    "fabric_audit_evidence_verified:true",
    "verification_storage_detached:true",
    "verification_compute_destroyed:true",
    "verification_storage_destroyed:true"
  ]);
});

test("production verifier preserves Workspace gateway cookies after token cleanup redirects", async () => {
  const requests = [];
  const chain = tkeChain();
  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    runId: "prod-run",
    packageId: "basic",
    workspaceUrlAttempts: 1,
    retryDelayMs: 0,
    fetchImpl: workspaceCookieGatewayFetch({ responses: chainResponses(chain), requests })
  });

  assert.equal(result.workspaceId, chain.workspace.id);
  assert.ok(requests.some((request) =>
    request.key === `GET ${chain.workspace.url}` &&
    request.redirect === "manual"
  ));
  assert.ok(requests.some((request) =>
    request.key === `GET ${workspaceUrl(chain.workspace.url, "/api/auth/user")}` &&
    request.cookie.includes("opl_ws_active=ws-tke-prod001") &&
    request.cookie.includes("opl_ws_ws-tke-prod001=share_tke_prod")
  ));
});

test("production verifier accepts Workspace URLs that are already token-free", async () => {
  const chain = tkeChain({ workspaceUrl: "https://workspace.medopl.cn/w/ws-tke-prod001/" });
  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    runId: "prod-run",
    packageId: "basic",
    workspaceUrlAttempts: 1,
    retryDelayMs: 0,
    fetchImpl: keyedFetch({ responses: chainResponses(chain) })
  });

  assert.ok(result.checks.some((check) => check.name === "workspace_url_token_scrubbed" && check.ok === true));
});

test("production verifier uses AionUI auto-login session cookie from auth user", async () => {
  const requests = [];
  const actions = [];
  const chain = tkeChain();

  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    runId: "prod-run",
    packageId: "basic",
    browserE2E: true,
    browserFactory: fakeBrowserFactory(actions),
    fetchImpl: keyedFetch({ responses: chainResponses(chain), requests })
  });

  assert.ok(result.ok);
  assert.ok(!requests.some((request) => request.key.includes("/login")));
  assert.ok(actions.find((action) => action[0] === "addCookies")?.[1].some((cookie) => (
    cookie.name === "aionui-session" && cookie.value === "api-session"
  )));
});

test("production verifier keeps workspace API calls on the discovered prefixed base", async () => {
  const requests = [];
  const chain = tkeChain();
  const responses = chainResponses(chain);
  const baseFetch = keyedFetch({ responses, requests });
  const prefixedBase = "https://workspace.medopl.cn/w/ws-tke-prod001/";
  const prefixedEndpoint = (path) => `${prefixedBase}${path.replace(/^\//, "")}`;
  const fetchImpl = async (url, options = {}) => {
    const parsed = new URL(String(url));
    const method = options.method || "GET";
    if (parsed.origin === "https://workspace.medopl.cn" && parsed.pathname.startsWith("/api/")) {
      requests.push({ key: `${method} ${String(url)}`, body: options.body ? JSON.parse(options.body) : null });
      return jsonResponse({ error: "root api unavailable" }, 404);
    }
    if (String(url) === prefixedEndpoint("/api/auth/user")) {
      requests.push({ key: `GET ${String(url)}` });
      return jsonResponse({ success: true, user: { id: "opl-webui-admin", username: "admin" } }, 200, new Headers({
        "content-type": "application/json",
        "set-cookie": "aionui-session=prefixed-session-token; Path=/; HttpOnly"
      }));
    }
    if (String(url) === prefixedEndpoint("/api/fs/write")) {
      requests.push({ key: `POST ${String(url)}`, body: options.body ? JSON.parse(options.body) : null });
      return jsonResponse({ success: true, data: true });
    }
    if (String(url) === prefixedEndpoint("/api/fs/read")) {
      const body = options.body ? JSON.parse(options.body) : null;
      requests.push({ key: `POST ${String(url)}`, body });
      return jsonResponse({ success: true, data: "opl persistence prod-run" });
    }
    return baseFetch(url, options);
  };

  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    runId: "prod-run",
    packageId: "basic",
    fetchImpl
  });

  assert.equal(result.ok, true);
  assert.ok(!requests.some((request) => request.key.includes("/login")));
  assert.ok(requests.some((request) => request.key === "GET https://workspace.medopl.cn/api/auth/user"));
  assert.ok(requests.some((request) => request.key === `GET ${prefixedEndpoint("/api/auth/user")}`));
  assert.ok(requests.some((request) => request.key === `POST ${prefixedEndpoint("/api/fs/write")}`));
  assert.ok(requests.some((request) => request.key === `POST ${prefixedEndpoint("/api/fs/read")}`));
});

test("production verifier waits for async compute provisioning before mounting storage", async () => {
  const requests = [];
  const chain = tkeChain();
  const provisioningCompute = {
    ...chain.compute,
    providerResourceId: "",
    instanceId: "",
    nodeName: "",
    privateIp: "",
    status: "provisioning",
    operationId: "op-compute-prod001"
  };
  const responses = chainResponses(chain);
  responses["POST /api/compute-allocations"] = provisioningCompute;
  responses[`GET /api/compute-allocations/${chain.compute.id}?accountId=pi-prod`] = provisioningCompute;
  responses[`GET /api/compute-allocations/${chain.compute.id}?accountId=pi-prod#2`] = chain.compute;

  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    runId: "prod-run",
    packageId: "basic",
    workspaceUrlAttempts: 2,
    retryDelayMs: 0,
    fetchImpl: keyedFetch({ responses, requests })
  });

  const requestKeys = requests.map((request) => request.key);
  const firstComputePoll = requestKeys.indexOf(`GET /api/compute-allocations/${chain.compute.id}?accountId=pi-prod`);
  const secondComputePoll = requestKeys.indexOf(`GET /api/compute-allocations/${chain.compute.id}?accountId=pi-prod#2`);
  const storageCreate = requestKeys.indexOf("POST /api/storage-volumes");
  assert.equal(result.ok, true);
  assert.ok(firstComputePoll > requestKeys.indexOf("POST /api/compute-allocations"));
  assert.ok(secondComputePoll > firstComputePoll);
  assert.ok(storageCreate > secondComputePoll);
});

test("production verifier can exercise one-person-lab-app through a real browser surface", async () => {
  const checks = [];
  const actions = [];

  await verifyWorkspaceBrowserUi({
    workspaceUrl: "https://workspace.medopl.cn/w/ws-browser001/?token=share_browser",
    runId: "browser-run",
    checks,
    browserFactory: fakeBrowserFactory(actions),
    screenshotDir: ""
  });

  assert.deepEqual(checks.map((check) => `${check.name}:${check.ok}`), [
    "workspace_browser_opened:true",
    "workspace_browser_file_uploaded:true",
    "workspace_browser_file_read:true",
    "workspace_browser_message_sent:true",
    "workspace_browser_reply_seen:true"
  ]);
  assert.deepEqual(actions.filter(([name]) => ["goto", "setInputFiles", "fill", "click", "close"].includes(name)).map(([name]) => name), [
    "goto",
    "setInputFiles",
    "click",
    "fill",
    "click",
    "close"
  ]);
  const assistantClick = actions.find((action) => action[0] === "click" && /@Research/.test(action[2] || ""));
  assert.ok(assistantClick, "browser verifier must select an assistant before sending the chat prompt");
});

test("production verifier can select the current visible production assistant card", async () => {
  const checks = [];
  const actions = [];

  await verifyWorkspaceBrowserUi({
    workspaceUrl: "https://workspace.medopl.cn/w/ws-browser001/?token=share_browser",
    runId: "browser-run",
    checks,
    browserFactory: fakeWorkspaceBrowserFactory(actions, {
      assistantLabels: ["@Med Auto Science", "@Med Auto Grant", "@RedCube AI"],
      domAssistantSelection: true,
      assistantReplies: true,
      roleAssistantSelection: false
    }),
    screenshotDir: ""
  });

  assert.ok(actions.some((action) => action[0] === "domAssistantClick" && action[1] === "@Med Auto Science"));
  assert.ok(checks.some((check) => check.name === "workspace_browser_message_sent" && check.ok === true));
});

test("production verifier primes browser workspace auth before opening one-person-lab-app", async () => {
  const checks = [];
  const actions = [];
  const workspaceUrlValue = "https://workspace.medopl.cn/w/ws-browser001/?token=share_browser";

  await verifyWorkspaceBrowserUi({
    workspaceUrl: workspaceUrlValue,
    workspaceAuth: {
      url: "https://workspace.medopl.cn/w/ws-browser001/",
      cookie: "opl_ws_active=ws-browser001; opl_ws_ws-browser001=share_browser"
    },
    runId: "browser-run",
    checks,
    browserFactory: fakeBrowserFactory(actions),
    screenshotDir: ""
  });

  assert.deepEqual(actions.find((action) => action[0] === "addCookies")?.[1], [
    { name: "opl_ws_active", value: "ws-browser001", domain: "workspace.medopl.cn", path: "/", secure: true },
    { name: "opl_ws_ws-browser001", value: "share_browser", domain: "workspace.medopl.cn", path: "/", secure: true }
  ]);
  assert.deepEqual(actions.filter(([name]) => name === "goto").map(([, url]) => url), [
    "https://workspace.medopl.cn/w/ws-browser001/"
  ]);
});

test("production verifier logs into AionUI before probing the workspace file input", async () => {
  const checks = [];
  const actions = [];

  await verifyWorkspaceBrowserUi({
    workspaceUrl: "https://workspace.medopl.cn/w/ws-browser001/?token=share_browser",
    workspaceAuth: {
      url: "https://workspace.medopl.cn/w/ws-browser001/",
      cookie: "opl_ws_active=ws-browser001; opl_ws_ws-browser001=share_browser",
      webuiUsername: "admin",
      webuiPassword: "ManagedWebuiPass2026!"
    },
    runId: "browser-run",
    checks,
    browserFactory: fakeAionUiLoginBrowserFactory(actions),
    screenshotDir: ""
  });

  assert.ok(actions.some((action) => action[0] === "fillLocator" && /username/i.test(action[1]) && action[2] === "admin"));
  assert.ok(actions.some((action) => action[0] === "fillLocator" && /password/i.test(action[1]) && action[2] === "ManagedWebuiPass2026!"));
  assert.ok(actions.findIndex((action) => action[0] === "click" && /Sign In/i.test(action[2])) < actions.findIndex((action) => action[0] === "setInputFiles"));
  assert.ok(checks.some((check) => check.name === "workspace_browser_webui_login" && check.ok === true));
});

test("production verifier fails message submission when assistant selection does not enter task mode", async () => {
  const checks = [];
  const actions = [];

  await assert.rejects(
    verifyWorkspaceBrowserUi({
      workspaceUrl: "https://workspace.medopl.cn/w/ws-browser001/?token=share_browser",
      runId: "browser-run",
      checks,
      browserFactory: fakeWorkspaceBrowserFactory(actions, { assistantSelectionTakesEffect: false }),
      screenshotDir: ""
    }),
    /workspace_browser_message_sent_failed/
  );
  assert.ok(checks.some((check) => check.name === "workspace_browser_message_sent" && check.ok === false));
  assert.ok(!checks.some((check) => check.name === "workspace_browser_reply_seen"));
});

test("production verifier does not treat the submitted user prompt as an assistant reply", async () => {
  const checks = [];
  const actions = [];

  await assert.rejects(
    verifyWorkspaceBrowserUi({
      workspaceUrl: "https://workspace.medopl.cn/w/ws-browser001/?token=share_browser",
      runId: "browser-run",
      checks,
      browserFactory: fakeWorkspaceBrowserFactory(actions),
      screenshotDir: ""
    }),
    /workspace_browser_reply_seen_failed/
  );
  assert.ok(checks.some((check) => check.name === "workspace_browser_message_sent" && check.ok === true));
  assert.ok(checks.some((check) => check.name === "workspace_browser_reply_seen" && check.ok === false));
});

test("production verifier fills the visible composer textbox before sending", async () => {
  const checks = [];
  const actions = [];

  await assert.rejects(
    verifyWorkspaceBrowserUi({
      workspaceUrl: "https://workspace.medopl.cn/w/ws-browser001/?token=share_browser",
      runId: "browser-run",
      checks,
      browserFactory: fakeWorkspaceBrowserFactory(actions, { firstTextboxIsNotComposer: true }),
      screenshotDir: ""
    }),
    /workspace_browser_reply_seen_failed/
  );
  assert.ok(actions.some((action) => action[0] === "fill" && action[3] === "last"));
  assert.ok(checks.some((check) => check.name === "workspace_browser_message_sent" && check.ok === true));
});

test("production verifier uses one-person-lab-app guid DOM contract for assistant send", async () => {
  const checks = [];
  const actions = [];

  await verifyWorkspaceBrowserUi({
    workspaceUrl: "https://workspace.medopl.cn/w/ws-browser001/?token=share_browser",
    runId: "browser-run",
    checks,
    browserFactory: fakeGuidDomBrowserFactory(actions),
    screenshotDir: ""
  });

  assert.ok(actions.some((action) => action[0] === "setHash" && action[1] === "#/guid"));
  assert.ok(actions.some((action) => action[0] === "domClick" && action[1] === "preset-pill-mas"));
  assert.ok(actions.some((action) => action[0] === "dispatchEvent" && action[1] === "input"));
  assert.ok(actions.some((action) => action[0] === "domClick" && action[1] === "guid-send-btn"));
  assert.deepEqual(checks.map((check) => `${check.name}:${check.ok}`), [
    "workspace_browser_opened:true",
    "workspace_browser_file_uploaded:true",
    "workspace_browser_file_read:true",
    "workspace_browser_message_sent:true",
    "workspace_browser_reply_seen:true"
  ]);
});

test("production verifier completes first-run model access before file upload", async () => {
  const checks = [];
  const actions = [];

  await verifyWorkspaceBrowserUi({
    workspaceUrl: "https://workspace.medopl.cn/w/ws-browser001/?token=share_browser",
    runId: "browser-run",
    checks,
    browserFactory: fakeGuidDomBrowserFactory(actions, { firstRun: true, setupButtonText: "Configure OPL Gateway" }),
    modelAccessKey: "test-access-key",
    screenshotDir: ""
  });

  assert.ok(actions.some((action) => action[0] === "fillLocator" && action[2] === "test-access-key"));
  assert.ok(actions.some((action) => action[0] === "roleClick" && /Configure OPL Gateway/i.test(action[2])));
  assert.ok(actions.findIndex((action) => action[0] === "roleClick" && /Configure OPL Gateway/i.test(action[2])) < actions.findIndex((action) => action[0] === "setInputFiles"));
  assert.deepEqual(checks.map((check) => `${check.name}:${check.ok}`), [
    "workspace_browser_opened:true",
    "workspace_browser_model_access_configured:true",
    "workspace_browser_file_uploaded:true",
    "workspace_browser_file_read:true",
    "workspace_browser_message_sent:true",
    "workspace_browser_reply_seen:true"
  ]);
});

test("production verifier runs optional browser UI checks after Workspace URL is ready", async () => {
  const requests = [];
  const actions = [];
  const chain = tkeChain();
  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    workspaceName: "Production Verification Lab",
    runId: "prod-run",
    packageId: "basic",
    browserE2E: true,
    browserFactory: fakeBrowserFactory(actions),
    fetchImpl: keyedFetch({ responses: chainResponses(chain), requests })
  });

  const browserChecks = result.checks
    .filter((check) => check.name.startsWith("workspace_browser_"))
    .map((check) => `${check.name}:${check.ok}`);
  assert.deepEqual(browserChecks, [
    "workspace_browser_opened:true",
    "workspace_browser_file_uploaded:true",
    "workspace_browser_file_read:true",
    "workspace_browser_message_sent:true",
    "workspace_browser_reply_seen:true"
  ]);
  assert.deepEqual(actions.filter(([name]) => name === "goto").map(([, url]) => url), [scrubbedWorkspaceUrl(chain.workspace.url)]);
  assert.ok(actions.find((action) => action[0] === "addCookies")?.[1].some((cookie) => (
    cookie.name === "aionui-session" && cookie.value === "api-session"
  )));
});

test("production verifier reports browser failure stage with resources, checks, and screenshot", async () => {
  const requests = [];
  const actions = [];
  const chain = tkeChain();
  let caught = null;

  try {
    await verifyProductionChain({
      origin: "https://console.oplcloud.cn",
      accountId: "pi-prod",
      workspaceName: "Production Verification Lab",
      runId: "prod-run",
      packageId: "basic",
      browserE2E: true,
      browserFactory: fakeBrowserFactory(actions, { failWaitAt: 5 }),
      screenshotDir: "/tmp/opl-production-verifier-test-screenshots",
      fetchImpl: keyedFetch({ responses: chainResponses(chain), requests })
    });
  } catch (error) {
    caught = error;
  }

  assert.equal(caught?.message, "workspace_browser_reply_seen_failed:Timeout 180000ms exceeded.");
  assert.equal(caught?.details?.stage, "workspace_browser_reply_seen");
  assert.match(caught?.details?.screenshotPath || "", /workspace-browser-e2e-prod-run-failure\.png$/);
  assert.deepEqual(caught?.resourceIds, {
    computeAllocationId: chain.compute.id,
    storageId: chain.storage.id,
    attachmentId: chain.attachment.id,
    workspaceId: chain.workspace.id
  });
  assert.deepEqual(caught?.checks?.map((check) => `${check.name}:${check.ok}`), [
    "production_readiness:true",
    "runtime_readiness:true",
    "compute_created:true",
    "storage_created:true",
    "storage_attached:true",
    "workspace_created:true",
    "workspace_runtime_status:true",
    "workspace_url:true",
    "workspace_url_token_scrubbed:true",
    "workspace_browser_opened:true",
    "workspace_browser_file_uploaded:true",
    "workspace_browser_file_read:true",
    "workspace_browser_message_sent:true",
    "workspace_browser_reply_seen:false"
  ]);
  assert.ok(actions.some((action) => action[0] === "screenshot" && action[1].endsWith("workspace-browser-e2e-prod-run-failure.png")));
  assert.deepEqual(requests.map((request) => request.key).slice(-3), [
    "POST /api/storage-attachments/detach",
    `POST /api/compute-allocations/${chain.compute.id}/destroy`,
    "POST /api/storage-volumes/destroy"
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
    runId: "prod-run",
    operatorToken: "operator-token",
    fetchImpl: keyedFetch({ responses, requests, responseHeaders })
  });

  assert.deepEqual(requests.map((request) => request.key).slice(0, 4), [
    "GET /api/production/readiness",
    "GET /api/runtime/readiness",
    "POST /api/auth/operator-login",
    "POST /api/billing/topups"
  ]);
  assert.equal(requests[2].operatorToken, "operator-token");
  assert.deepEqual(requests[2].body, {});
  for (const request of requests.filter((item) => item.key.startsWith("POST /api/") && item.key !== "POST /api/auth/operator-login")) {
    assert.match(request.cookie, /opl_console_session=operator-session/);
    assert.equal(request.csrf, "csrf-auth");
  }
});

test("production verifier reports safe ledger mismatch details", async () => {
  const chain = tkeChain();
  const responses = chainResponses(chain);
  responses["GET /api/state?accountId=pi-prod"] = {
    ...responses["GET /api/state?accountId=pi-prod"],
    walletTransactions: []
  };

  await assert.rejects(
    verifyProductionChain({
      origin: "https://console.oplcloud.cn",
      accountId: "pi-prod",
      workspaceName: "Production Verification Lab",
      runId: "prod-run",
      packageId: "basic",
      fetchImpl: keyedFetch({ responses })
    }),
    (error) => {
      assert.equal(error.message, "ledger_and_wallet_transactions_verified_failed");
      assert.deepEqual(error.details?.missingChecks, [
        "compute_wallet_transaction",
        "storage_wallet_transaction",
        "compute_wallet_after",
        "storage_wallet_after"
      ]);
      return true;
    }
  );
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
  responses["POST /api/workspaces/runtime-status#3"] = readyRuntimeStatus(chain.replacementWorkspace);
  responses[`GET ${chain.workspace.url}`] = "bad gateway";
  responses[`GET ${chain.workspace.url}#2`] = "<html>one-person-lab-app</html>";
  const baseFetch = keyedFetch({ responses, requests });

  const result = await verifyProductionChain({
    origin: "https://console.oplcloud.cn",
    accountId: "pi-prod",
    runId: "prod-run",
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

test("production verifier reports failed runtime checks with ok state", async () => {
  const chain = tkeChain();
  const responses = chainResponses(chain);
  responses["POST /api/workspaces/runtime-status"] = {
    provider: "tencent-tke",
    workspaceId: chain.workspace.id,
    ready: false,
    checks: [
      { name: "deployment_ready", ok: false, details: { phase: "Pending", containers: [{ name: "workspace", state: "waiting", reason: "CrashLoopBackOff" }] } },
      { name: "pvc_bound", ok: true },
      { name: "service_endpoints_ready", ok: false }
    ]
  };

  await assert.rejects(
    verifyProductionChain({
      origin: "https://console.oplcloud.cn",
      accountId: "pi-prod",
      runId: "prod-run",
      workspaceUrlAttempts: 1,
      retryDelayMs: 0,
      fetchImpl: keyedFetch({ responses })
    }),
    (error) => {
      assert.equal(error.message, "workspace_runtime_status_failed");
      assert.deepEqual(error.details?.failedChecks, ["deployment_ready", "service_endpoints_ready"]);
      assert.deepEqual(error.details?.runtimeChecks, [
        { name: "deployment_ready", ok: false, details: { phase: "Pending", containers: [{ name: "workspace", state: "waiting", reason: "CrashLoopBackOff" }] } },
        { name: "pvc_bound", ok: true },
        { name: "service_endpoints_ready", ok: false }
      ]);
      return true;
    }
  );
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

test("production verifier can leave failed resources for live diagnosis", async () => {
  const chain = tkeChain();
  const responses = chainResponses(chain);
  responses["POST /api/auth/operator-login"] = { accountId: "operator", role: "operator" };
  responses["POST /api/workspaces/runtime-status"] = {
    ready: false,
    checks: [{ name: "deployment_ready", ok: false }]
  };
  const requests = [];

  await assert.rejects(
    verifyProductionChain({
      origin: "https://cloud.medopl.cn",
      operatorToken: "operator-token",
      runId: "diag",
      workspaceUrlAttempts: 1,
      retryDelayMs: 0,
      cleanupOnFailure: false,
      fetchImpl: keyedFetch({
        responses,
        requests,
        consoleOrigin: "https://cloud.medopl.cn",
        statusByKey: { [`GET ${chain.workspace.url}`]: 503 }
      })
    }),
    (error) => {
      assert.equal(error.cleanupSkipped, true);
      return true;
    }
  );

  assert.equal(requests.some((request) => request.key === `POST /api/compute-allocations/${chain.compute.id}/destroy`), false);
  assert.equal(requests.some((request) => request.key === "POST /api/storage-volumes/destroy"), false);
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
  const payload = JSON.parse(stderr);
  assert.equal(payload.ok, false);
  assert.equal(payload.error, "workspace_url_failed:502:bad gateway");
  assert.deepEqual(payload.resourceIds, {
    computeAllocationId: chain.compute.id,
    storageId: chain.storage.id,
    attachmentId: chain.attachment.id,
    workspaceId: chain.workspace.id
  });
  assert.deepEqual(payload.checks.map((check) => `${check.name}:${check.ok}`), [
    "production_readiness:true",
    "runtime_readiness:true",
    "compute_created:true",
    "storage_created:true",
    "storage_attached:true",
    "workspace_created:true",
    "workspace_runtime_status:true"
  ]);
  assert.deepEqual(payload.cleanupErrors, [
    "detach_storage:request_failed:POST:/api/storage-attachments/detach:500:detach_failed",
    `destroy_compute:request_failed:POST:/api/compute-allocations/${chain.compute.id}/destroy:500:destroy_compute_failed`,
    "destroy_storage:request_failed:POST:/api/storage-volumes/destroy:500:destroy_storage_failed"
  ]);
});

test("production verifier CLI preserves safe provider failure details", async () => {
  let stdout = "";
  let stderr = "";
  const code = await runProductionVerifierCli({
    argv: ["--origin", "https://console.oplcloud.cn", "--account", "pi-prod", "--run-id", "provider-fail"],
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: keyedFetch({
      responses: {
        "GET /api/production/readiness": { ready: true },
        "GET /api/runtime/readiness": { ready: true },
        "POST /api/billing/topups": { ok: true },
        "POST /api/compute-allocations": {
          error: "tencent_describe_node_pool_failed",
          safeMessage: "node pool not found: np-basic",
          providerRequestId: "req-describe",
          retryable: false
        }
      },
      statusByKey: {
        "POST /api/compute-allocations": 400
      }
    })
  });

  assert.equal(code, 1);
  assert.equal(stdout, "");
  assert.deepEqual(JSON.parse(stderr), {
    ok: false,
    error: "request_failed:POST:/api/compute-allocations:400:tencent_describe_node_pool_failed",
    safeMessage: "node pool not found: np-basic",
    providerRequestId: "req-describe",
    retryable: false
  });
});
