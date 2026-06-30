import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";

import { createOplCloud } from "./src/opl-cloud.js";
import { FakeRuntimeProvider } from "./src/runtime-providers/fake.js";
import { JsonFileStore } from "./src/store.js";

const root = fileURLToPath(new URL("../..", import.meta.url));
const publicDir = join(root, "dist");
const port = Number(process.env.PORT ?? 8787);
const dataPath = process.env.OPL_CLOUD_DATA_PATH ?? join(root, ".runtime", "opl-cloud-state.json");

export const service = createOplCloud({
  store: new JsonFileStore(dataPath),
  runtimeProvider: new FakeRuntimeProvider(),
  pricing: {
    serverHourly: { basic: 1, pro: 4 },
    diskGbMonth: 0.2,
    markup: 0.1
  }
});

function sendJson(response, status, payload) {
  response.writeHead(status, { "content-type": "application/json; charset=utf-8" });
  response.end(`${JSON.stringify(payload, null, 2)}\n`);
}

async function readJson(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  const raw = Buffer.concat(chunks).toString("utf8");
  return raw.trim() ? JSON.parse(raw) : {};
}

async function handleApi(request, response, pathname) {
  try {
    if (request.method === "GET" && pathname === "/api/state") {
      const url = new URL(request.url, "http://localhost");
      return sendJson(response, 200, await service.getState(url.searchParams.get("accountId") ?? "pi-alpha"));
    }

    const body = await readJson(request);
    const routes = {
      "POST /api/accounts/credit": () => service.creditAccount(body),
      "POST /api/workspaces": () => service.createWorkspace(body),
      "POST /api/workspaces/stop-server": () => service.stopServer(body),
      "POST /api/workspaces/restart-server": () => service.restartServer(body),
      "POST /api/workspaces/destroy-server": () => service.destroyServer(body),
      "POST /api/workspaces/destroy-disk": () => service.destroyDisk(body),
      "POST /api/workspaces/reset-token": () => service.resetWorkspaceToken(body),
      "POST /api/workspaces/delete-token": () => service.deleteWorkspaceToken(body)
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

async function serveStatic(response, pathname) {
  const safePath = normalize(pathname === "/" ? "index.html" : pathname.slice(1)).replace(/^(\.\.(\/|\\|$))+/, "");
  const fullPath = join(publicDir, safePath);
  try {
    const content = await readFile(fullPath);
    response.writeHead(200, { "content-type": contentTypes[extname(fullPath)] ?? "application/octet-stream" });
    response.end(content);
  } catch {
    response.writeHead(404, { "content-type": "text/plain; charset=utf-8" });
    response.end("Not found\n");
  }
}

const server = createServer((request, response) => {
  const url = new URL(request.url, "http://localhost");
  if (url.pathname.startsWith("/api/")) return handleApi(request, response, url.pathname);
  return serveStatic(response, url.pathname);
});

server.listen(port, () => {
  console.log(`OPL Cloud API listening on http://127.0.0.1:${port}`);
  console.log(`State file: ${dataPath}`);
});
