import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { createRuntimeProvider } from "../../fabric/src/runtime-provider-factory.js";
import { createOplCloud } from "../src/opl-cloud.js";
import { productionReadiness } from "../src/production-readiness.js";
import { JsonFileStore, PostgresStore } from "../src/store.js";

const root = fileURLToPath(new URL("../../..", import.meta.url));
const publicDir = join(root, "dist");
const port = Number(process.env.PORT ?? 8787);
const dataPath = process.env.OPL_CLOUD_DATA_PATH ?? join(root, ".runtime", "opl-cloud-state.json");

function numberFromEnv(name, fallback) {
  const value = Number(process.env[name]);
  return Number.isFinite(value) ? value : fallback;
}

export function createStoreFromEnv(env = process.env) {
  if (env.DATABASE_URL) return new PostgresStore({ connectionString: env.DATABASE_URL });
  return new JsonFileStore(env.OPL_CLOUD_DATA_PATH ?? dataPath);
}

export const service = createOplCloud({
  store: createStoreFromEnv(process.env),
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
  if (request.method !== "GET") {
    return sendHtml(response, 405, "<!doctype html><title>Method not allowed</title><h1>Method not allowed</h1>");
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
    const runtimeTarget = workspace.docker?.localUrl || workspace.docker?.composePath || "runtime target pending";
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
      <p>This URL token is valid. Local Docker mode has provisioned the OPL Workspace runtime assets below. Production mode connects this entry to Caddy and proxies directly to the running one-person-lab-app container.</p>
      <dl>
        <dt>Status</dt><dd>${workspace.state}</dd>
        <dt>Server</dt><dd>${workspace.server.status}</dd>
        <dt>Docker</dt><dd>${workspace.docker.status}</dd>
        <dt>Disk</dt><dd>${workspace.disk.status} / ${workspace.disk.mountPath}</dd>
        <dt>Runtime</dt><dd><code>${runtimeTarget}</code></dd>
      </dl>
    </main>
  </body>
</html>`);
  } catch (error) {
    return sendHtml(response, 403, `<!doctype html><title>OPL Workspace unavailable</title><h1>OPL Workspace unavailable</h1><p>${error.message}</p>`);
  }
}

async function handleApi(request, response, pathname, appService, operatorSummaryToken = process.env.OPL_OPERATOR_SUMMARY_TOKEN) {
  try {
    if (request.method === "GET" && pathname === "/api/state") {
      const url = new URL(request.url, "http://localhost");
      return sendJson(response, 200, await appService.getState(url.searchParams.get("accountId") ?? "pi-alpha"));
    }
    if (request.method === "GET" && pathname === "/api/runtime/readiness") {
      return sendJson(response, 200, await appService.runtimeReadiness());
    }
    if (request.method === "GET" && pathname === "/api/production/readiness") {
      return sendJson(response, 200, await appService.productionReadiness());
    }
    if (request.method === "GET" && pathname === "/api/operator/summary") {
      const url = new URL(request.url, "http://localhost");
      const providedToken = request.headers["x-opl-operator-token"] || url.searchParams.get("operatorToken") || "";
      if (!operatorSummaryToken) return sendJson(response, 403, { ok: false, error: "operator_summary_token_not_configured" });
      if (providedToken !== operatorSummaryToken) return sendJson(response, 403, { ok: false, error: "operator_summary_token_invalid" });
      return sendJson(response, 200, await appService.operatorSummary({
        accountId: url.searchParams.get("accountId") || null
      }));
    }

    const body = await readJson(request);
    const routes = {
      "POST /api/accounts/credit": () => appService.creditAccount(body),
      "POST /api/workspaces": () => appService.createWorkspace(body),
      "POST /api/workspaces/stop-server": () => appService.stopServer(body),
      "POST /api/workspaces/restart-server": () => appService.restartServer(body),
      "POST /api/workspaces/destroy-server": () => appService.destroyServer(body),
      "POST /api/workspaces/destroy-disk": () => appService.destroyDisk(body),
      "POST /api/workspaces/runtime-status": () => appService.runtimeStatus(body),
      "POST /api/workspaces/reset-token": () => appService.resetWorkspaceToken(body),
      "POST /api/workspaces/delete-token": () => appService.deleteWorkspaceToken(body),
      "POST /api/billing/settle": () => appService.settleBilling(body)
    };
    const handler = routes[`${request.method} ${pathname}`];
    if (!handler) return sendJson(response, 404, { ok: false, error: "route_not_found" });
    return sendJson(response, 200, await handler());
  } catch (error) {
    return sendJson(response, 400, { ok: false, error: error.message });
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
    response.end("Not found\n");
  }
}

export function createRequestHandler({ appService = service, staticDir = publicDir, operatorSummaryToken = process.env.OPL_OPERATOR_SUMMARY_TOKEN } = {}) {
  return (request, response) => {
    const url = new URL(request.url, "http://localhost");
    if (url.pathname.startsWith("/api/")) return handleApi(request, response, url.pathname, appService, operatorSummaryToken);
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
