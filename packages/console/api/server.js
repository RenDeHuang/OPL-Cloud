import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { createRuntimeProvider } from "../../fabric/src/runtime-provider-factory.js";
import { createAuthController } from "./auth.js";
import { createOplCloud } from "../src/opl-cloud.js";
import { productionReadiness } from "../src/production-readiness.js";
import { JsonFileStore, PostgresStore } from "../src/store.js";

const root = fileURLToPath(new URL("../../..", import.meta.url));
const publicDir = join(root, "dist");
const port = Number(process.env.PORT ?? 8787);
const dataPath = process.env.OPL_CLOUD_DATA_PATH ?? join(root, ".runtime", "opl-cloud-state.json");

export { createAuthController };

function numberFromEnv(name, fallback) {
  const value = Number(process.env[name]);
  return Number.isFinite(value) ? value : fallback;
}

export function createStoreFromEnv(env = process.env) {
  if (env.DATABASE_URL) return new PostgresStore({ connectionString: env.DATABASE_URL });
  return new JsonFileStore(env.OPL_CLOUD_DATA_PATH ?? dataPath);
}

export const appStore = createStoreFromEnv(process.env);

export const service = createOplCloud({
  store: appStore,
  runtimeProvider: createRuntimeProvider({
    env: process.env,
    rootDir: join(root, ".runtime", "workspaces")
  }),
  pricing: {
    computeHourly: {
      basic: numberFromEnv("OPL_BASIC_COMPUTE_HOURLY_CNY", 1),
      pro: numberFromEnv("OPL_PRO_COMPUTE_HOURLY_CNY", 4)
    },
    storageGbMonth: numberFromEnv("OPL_STORAGE_GB_MONTH_CNY", 0.2),
    markup: numberFromEnv("OPL_BILLING_MARKUP", 0.2)
  },
  productionReadiness: () => productionReadiness({ env: process.env })
});

const defaultAuth = createAuthController({ env: process.env, store: appStore });

function sendJson(response, status, payload) {
  response.writeHead(status, { "content-type": "application/json; charset=utf-8" });
  response.end(`${JSON.stringify(payload, null, 2)}\n`);
}

function sendHtml(response, status, html) {
  response.writeHead(status, { "content-type": "text/html; charset=utf-8" });
  response.end(html);
}

async function readJson(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  const raw = Buffer.concat(chunks).toString("utf8");
  return raw.trim() ? JSON.parse(raw) : {};
}

async function handleWorkspaceUrl(request, response, pathname, searchParams, appService) {
  function statusText(value) {
    const labels = {
      running: "运行中",
      stopped: "已停止",
      destroyed: "已销毁",
      failed: "失败",
      active: "有效",
      attached_retained: "已挂载并保留",
      detached_retained: "已卸载并保留"
    };
    return labels[value] || value;
  }

  function workspaceErrorText(value) {
    const labels = {
      workspace_not_found: "未找到 OPL Workspace。",
      workspace_token_inactive: "OPL Workspace 访问令牌已失效。",
      workspace_token_invalid: "OPL Workspace 访问令牌无效。"
    };
    return labels[value] || value;
  }

  if (request.method !== "GET") {
    return sendHtml(response, 405, "<!doctype html><title>方法不允许</title><h1>方法不允许</h1>");
  }

  const slug = pathname.split("/").filter(Boolean)[1];
  try {
    const workspace = await appService.resolveWorkspaceAccess({
      slug,
      token: searchParams.get("token") ?? ""
    });
    const isRunning = workspace.state === "running" && workspace.server.status === "running" && workspace.access.tokenStatus === "active";
    if (isRunning && workspace.docker?.localUrl) {
      response.writeHead(302, { location: workspace.docker.localUrl });
      return response.end();
    }
    const runtimeTarget = workspace.docker?.localUrl || workspace.docker?.composePath || "运行时目标待生成";
    return sendHtml(response, isRunning ? 200 : 409, `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>${workspace.name} - OPL Workspace</title>
    <style>
      body { margin: 0; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: #f6f7f9; color: #111827; }
      main { max-width: 760px; margin: 8vh auto; padding: 32px; background: #fff; border: 1px solid #d9dee7; border-radius: 8px; }
      h1 { margin: 0 0 8px; font-size: 28px; }
      p { line-height: 1.55; color: #4b5563; }
      dl { display: grid; grid-template-columns: 160px 1fr; gap: 12px 16px; margin: 24px 0; }
      dt { color: #6b7280; }
      dd { margin: 0; font-weight: 700; word-break: break-all; }
      code { background: #eef2f7; padding: 3px 6px; border-radius: 5px; }
    </style>
  </head>
  <body>
    <main>
      <h1>${workspace.name}</h1>
      <p>此 URL 令牌有效。Local Docker 模式已生成下方 OPL Workspace 运行时资产。生产模式通过 TKE Ingress 路由到 Workspace Service 和正在运行的 one-person-lab-app Deployment。</p>
      <dl>
        <dt>状态</dt><dd>${statusText(workspace.state)}</dd>
        <dt>计算</dt><dd>${statusText(workspace.server.status)}</dd>
        <dt>Docker</dt><dd>${statusText(workspace.docker.status)}</dd>
        <dt>存储</dt><dd>${statusText(workspace.disk.status)} / ${workspace.disk.mountPath}</dd>
        <dt>运行时</dt><dd><code>${runtimeTarget}</code></dd>
      </dl>
    </main>
  </body>
</html>`);
  } catch (error) {
    return sendHtml(response, 403, `<!doctype html><title>OPL Workspace 不可用</title><h1>OPL Workspace 不可用</h1><p>${workspaceErrorText(error.message)}</p>`);
  }
}

function errorStatus(error) {
  return Number.isInteger(error.status) ? error.status : 400;
}

function scopedAccountId(auth, session, requestedAccountId) {
  return auth ? auth.accountIdFor(session.user, requestedAccountId) : requestedAccountId;
}

function scopedWorkspaceInput(auth, session, body) {
  return auth ? auth.workspaceInputFor(session.user, body) : body;
}

function requireAdmin(auth, session) {
  if (auth) auth.requireAdmin(session.user);
}

async function handleApi(request, response, pathname, appService, operatorSummaryToken = process.env.OPL_OPERATOR_SUMMARY_TOKEN, auth = null) {
  try {
    if (request.method === "GET" && pathname === "/api/healthz") {
      return sendJson(response, 200, { ok: true, service: "opl-console" });
    }
    if (auth && request.method === "POST" && pathname === "/api/auth/login") {
      return sendJson(response, 200, await auth.login(await readJson(request), { request, response }));
    }
    if (auth && request.method === "POST" && pathname === "/api/auth/logout") {
      await auth.requireSession(request, { requireCsrf: true });
      return sendJson(response, 200, await auth.logout(request, response));
    }
    if (auth && request.method === "GET" && pathname === "/api/auth/me") {
      const session = await auth.requireSession(request);
      return sendJson(response, 200, {
        user: session.user,
        csrfToken: session.csrfToken
      });
    }
    if (!auth && pathname.startsWith("/api/auth/")) {
      return sendJson(response, 404, { ok: false, error: "route_not_found" });
    }

    const session = auth
      ? await auth.requireSession(request, { requireCsrf: request.method !== "GET" && request.method !== "HEAD" })
      : null;

    if (request.method === "GET" && pathname === "/api/state") {
      const url = new URL(request.url, "http://localhost");
      const requestedAccountId = url.searchParams.get("accountId") || (auth ? "" : "pi-alpha");
      const accountId = scopedAccountId(auth, session, requestedAccountId);
      return sendJson(response, 200, await appService.getState(accountId));
    }
    if (request.method === "GET" && pathname === "/api/runtime/readiness") {
      requireAdmin(auth, session);
      return sendJson(response, 200, await appService.runtimeReadiness());
    }
    if (request.method === "GET" && pathname === "/api/production/readiness") {
      requireAdmin(auth, session);
      return sendJson(response, 200, await appService.productionReadiness());
    }
    if (request.method === "GET" && pathname === "/api/operator/summary") {
      const url = new URL(request.url, "http://localhost");
      const providedToken = request.headers["x-opl-operator-token"] || url.searchParams.get("operatorToken") || "";
      const adminSession = auth && auth.isAdmin(session.user);
      if (!adminSession) {
        if (!operatorSummaryToken) return sendJson(response, 403, { ok: false, error: "operator_summary_token_not_configured" });
        if (providedToken !== operatorSummaryToken) return sendJson(response, 403, { ok: false, error: "operator_summary_token_invalid" });
      }
      return sendJson(response, 200, await appService.operatorSummary({
        accountId: url.searchParams.get("accountId") || null
      }));
    }
    if (request.method === "GET" && pathname === "/api/management/state") {
      requireAdmin(auth, session);
      const url = new URL(request.url, "http://localhost");
      return sendJson(response, 200, await appService.managementState({
        organizationId: url.searchParams.get("organizationId") || ""
      }));
    }
    if (request.method === "GET" && pathname === "/api/ledger/task-receipts") {
      const url = new URL(request.url, "http://localhost");
      return sendJson(response, 200, await appService.taskEvidenceReceipts({
        accountId: scopedAccountId(auth, session, url.searchParams.get("accountId") || ""),
        workspaceId: url.searchParams.get("workspaceId") || null,
        taskId: url.searchParams.get("taskId") || null
      }));
    }

    const body = await readJson(request);
    const routes = {
      "POST /api/accounts/credit": () => {
        requireAdmin(auth, session);
        return appService.creditAccount(body);
      },
      "POST /api/organizations": () => {
        requireAdmin(auth, session);
        return appService.createOrganization(body);
      },
      "POST /api/users": () => {
        requireAdmin(auth, session);
        return appService.createUser(body);
      },
      "POST /api/organizations/members": () => {
        requireAdmin(auth, session);
        return appService.addOrganizationMember(body);
      },
      "POST /api/workspaces": () => appService.createWorkspace(scopedWorkspaceInput(auth, session, body)),
      "POST /api/workspaces/stop-server": () => appService.stopServer(scopedWorkspaceInput(auth, session, body)),
      "POST /api/workspaces/restart-server": () => appService.restartServer(scopedWorkspaceInput(auth, session, body)),
      "POST /api/workspaces/destroy-server": () => appService.destroyServer(scopedWorkspaceInput(auth, session, body)),
      "POST /api/workspaces/destroy-disk": () => appService.destroyDisk(scopedWorkspaceInput(auth, session, body)),
      "POST /api/workspaces/runtime-status": () => {
        requireAdmin(auth, session);
        return appService.runtimeStatus(body);
      },
      "POST /api/workspaces/storage-backups": () => appService.createStorageBackup(scopedWorkspaceInput(auth, session, body)),
      "POST /api/workspaces/restore-storage-backup": () => appService.restoreWorkspaceFromBackup(scopedWorkspaceInput(auth, session, body)),
      "POST /api/workspaces/prune-storage-backups": () => appService.pruneStorageBackups(scopedWorkspaceInput(auth, session, body)),
      "POST /api/workspaces/reset-token": () => appService.resetWorkspaceToken(scopedWorkspaceInput(auth, session, body)),
      "POST /api/workspaces/delete-token": () => appService.deleteWorkspaceToken(scopedWorkspaceInput(auth, session, body)),
      "POST /api/billing/settle": () => appService.settleBilling(scopedWorkspaceInput(auth, session, body)),
      "POST /api/billing/reconciliation": () => {
        requireAdmin(auth, session);
        return appService.recordBillingReconciliation(body);
      },
      "POST /api/ledger/task-receipts": () => appService.recordTaskEvidenceReceipt(scopedWorkspaceInput(auth, session, body))
    };
    const handler = routes[`${request.method} ${pathname}`];
    if (!handler) return sendJson(response, 404, { ok: false, error: "route_not_found" });
    return sendJson(response, 200, await handler());
  } catch (error) {
    return sendJson(response, errorStatus(error), { ok: false, error: error.message });
  }
}

const contentTypes = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml; charset=utf-8"
};

async function serveStatic(response, pathname, staticDir = publicDir) {
  const safePath = normalize(pathname === "/" ? "index.html" : pathname.slice(1)).replace(/^(\.\.(\/|\\|$))+/, "");
  const fullPath = join(staticDir, safePath);
  try {
    const content = await readFile(fullPath);
    response.writeHead(200, { "content-type": contentTypes[extname(fullPath)] ?? "application/octet-stream" });
    response.end(content);
  } catch {
    response.writeHead(404, { "content-type": "text/plain; charset=utf-8" });
    response.end("未找到\n");
  }
}

export function createRequestHandler({ appService = service, staticDir = publicDir, operatorSummaryToken = process.env.OPL_OPERATOR_SUMMARY_TOKEN, auth = appService === service ? defaultAuth : null } = {}) {
  return (request, response) => {
    const url = new URL(request.url, "http://localhost");
    if (url.pathname.startsWith("/api/")) return handleApi(request, response, url.pathname, appService, operatorSummaryToken, auth);
    if (url.pathname.startsWith("/workspaces/")) {
      return handleWorkspaceUrl(request, response, url.pathname, url.searchParams, appService);
    }
    return serveStatic(response, url.pathname, staticDir);
  };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const server = createServer(createRequestHandler());
  server.listen(port, () => {
    console.log(`OPL Cloud API listening on http://127.0.0.1:${port}`);
    console.log(`State file: ${dataPath}`);
  });
}
