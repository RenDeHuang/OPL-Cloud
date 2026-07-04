import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { connect } from "node:net";
import { extname, join, normalize } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { createRuntimeProvider } from "../../fabric/src/index.js";
import { createAuthController } from "./auth.js";
import { buildApiRoutes } from "./routes/index.js";
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

export const productionPricingDefaults = {
  computeHourly: {
    basic: 0.39,
    pro: 3.09
  },
  storageGbMonth: 0.36,
  markup: 0.2
};

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
      basic: numberFromEnv("OPL_BASIC_COMPUTE_HOURLY_CNY", productionPricingDefaults.computeHourly.basic),
      pro: numberFromEnv("OPL_PRO_COMPUTE_HOURLY_CNY", productionPricingDefaults.computeHourly.pro)
    },
    storageGbMonth: numberFromEnv("OPL_STORAGE_GB_MONTH_CNY", productionPricingDefaults.storageGbMonth),
    markup: numberFromEnv("OPL_BILLING_MARKUP", productionPricingDefaults.markup)
  },
  productionReadiness: () => productionReadiness({ env: process.env })
});

const defaultAuth = createAuthController({ env: process.env, store: appStore });
const publicApiRoutes = new Set([
  "GET /api/healthz",
  "GET /api/runtime/readiness",
  "GET /api/production/readiness"
]);
const activeWorkspaceCookieName = "opl_ws_active";
const hopByHopHeaders = new Set([
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade"
]);

function sendJson(response, status, payload) {
  response.writeHead(status, { "content-type": "application/json; charset=utf-8" });
  response.end(`${JSON.stringify(payload, null, 2)}\n`);
}

function errorPayload(error) {
  return {
    ok: false,
    error: error.message,
    ...(error.safeMessage ? { safeMessage: error.safeMessage } : {}),
    ...(error.providerRequestId || typeof error.retryable === "boolean" ? {
      provider: {
        ...(error.providerRequestId ? { requestId: error.providerRequestId } : {}),
        ...(typeof error.retryable === "boolean" ? { retryable: error.retryable } : {})
      }
    } : {}),
    ...(error.providerRequestId ? { providerRequestId: error.providerRequestId } : {}),
    ...(typeof error.retryable === "boolean" ? { retryable: error.retryable } : {}),
    ...(Array.isArray(error.missingEnv) ? { missingEnv: error.missingEnv } : {})
  };
}

function sendHtml(response, status, html) {
  response.writeHead(status, { "content-type": "text/html; charset=utf-8" });
  response.end(html);
}

function escapeHtml(value = "") {
  return String(value)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function workspaceUnavailableHtml({
  title = "OPL Workspace 不可用",
  heading = "工作区已释放或资源不可用",
  message = "返回控制台检查资源状态；存储数据是否保留请以控制台资源状态为准。"
} = {}) {
  return `<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>${escapeHtml(title)}</title>
    <style>
      body { margin: 0; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: #f6f7f9; color: #111827; }
      main { max-width: 680px; margin: 10vh auto; padding: 28px; background: #fff; border: 1px solid #d9dee7; border-radius: 8px; }
      h1 { margin: 0 0 10px; font-size: 26px; }
      p { margin: 0; line-height: 1.65; color: #4b5563; }
    </style>
  </head>
  <body>
    <main>
      <h1>${escapeHtml(heading)}</h1>
      <p>${escapeHtml(message)}</p>
    </main>
  </body>
</html>`;
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
      <p>此 URL 令牌有效。TKE/CVM runtime 已绑定下方计算、存储和 one-person-lab-app 服务，入口通过 Workspace 网关路由。</p>
      <dl>
        <dt>状态</dt><dd>${statusText(workspace.state)}</dd>
        <dt>计算</dt><dd>${statusText(workspace.server.status)}</dd>
        <dt>运行容器</dt><dd>${statusText(workspace.docker.status)}</dd>
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

function workspaceAccessCookieName(workspaceId) {
  return `opl_ws_${String(workspaceId).replace(/[^A-Za-z0-9_-]/g, "_")}`;
}

function parseCookies(header = "") {
  return Object.fromEntries(String(header)
    .split(";")
    .map((part) => part.trim())
    .filter(Boolean)
    .map((part) => {
      const separator = part.indexOf("=");
      const key = separator >= 0 ? part.slice(0, separator) : part;
      const value = separator >= 0 ? part.slice(separator + 1) : "";
      try {
        return [key, decodeURIComponent(value)];
      } catch {
        return [key, value];
      }
    }));
}

function serializeCookie({ name, value, path = "/" }) {
  return [
    `${name}=${encodeURIComponent(value)}`,
    `Path=${path}`,
    "HttpOnly",
    "Secure",
    "SameSite=Lax",
    "Max-Age=2592000"
  ].join("; ");
}

function workspaceAccessCookies({ workspaceId, token }) {
  return [
    serializeCookie({ name: activeWorkspaceCookieName, value: workspaceId }),
    serializeCookie({ name: workspaceAccessCookieName(workspaceId), value: token })
  ];
}

function runtimeCookieHeader(header = "") {
  return String(header)
    .split(";")
    .map((part) => part.trim())
    .filter(Boolean)
    .filter((part) => {
      const name = part.split("=")[0];
      return name !== activeWorkspaceCookieName && !name.startsWith("opl_ws_");
    })
    .join("; ");
}

function headerEntries(headers) {
  return Object.entries(headers || {}).flatMap(([name, value]) => {
    if (Array.isArray(value)) return value.map((item) => [name, item]);
    return value === undefined ? [] : [[name, value]];
  });
}

function proxyRequestHeaders(request) {
  const headers = {};
  for (const [name, value] of headerEntries(request.headers)) {
    const key = name.toLowerCase();
    if (hopByHopHeaders.has(key)) continue;
    if (key === "host" || key === "cookie" || key === "accept-encoding") continue;
    headers[name] = value;
  }
  const runtimeCookies = runtimeCookieHeader(request.headers.cookie || "");
  if (runtimeCookies) headers.cookie = runtimeCookies;
  headers["accept-encoding"] = "identity";
  return headers;
}

function proxyResponseHeaders(upstreamResponse) {
  const headers = {};
  upstreamResponse.headers.forEach((value, name) => {
    const key = name.toLowerCase();
    if (hopByHopHeaders.has(key)) return;
    if (key === "content-encoding" || key === "content-length") return;
    if (key === "set-cookie") return;
    headers[name] = value;
  });
  const setCookie = upstreamResponse.headers.getSetCookie?.() || [];
  if (setCookie.length) headers["set-cookie"] = setCookie;
  return headers;
}

function shouldBufferWorkspaceResponse({ request, upstreamPath }) {
  return request.method === "GET" && (upstreamPath === "/" || upstreamPath.startsWith("/assets/"));
}

function appendHeaderToken(value, token) {
  const tokens = String(value || "")
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
  return tokens.some((item) => item.toLowerCase() === token.toLowerCase())
    ? tokens.join(", ")
    : [...tokens, token].join(", ");
}

function workspaceStaticBody({ headers, body }) {
  headers["cache-control"] = appendHeaderToken(headers["cache-control"], "no-transform");
  headers["content-length"] = String(body.byteLength);
  return body;
}

function appendSetCookie(headers, cookies) {
  const nextCookies = Array.isArray(cookies) ? cookies : [cookies].filter(Boolean);
  if (!nextCookies.length) return;
  const existing = headers["set-cookie"];
  headers["set-cookie"] = [
    ...(Array.isArray(existing) ? existing : existing ? [existing] : []),
    ...nextCookies
  ];
}

function serviceNameFromRef(value = "") {
  return String(value || "").split("/").pop();
}

function workspaceRuntimeBaseUrl(workspace) {
  if (workspace.docker?.localUrl) return workspace.docker.localUrl;
  const serviceName = serviceNameFromRef(workspace.docker?.service || workspace.server?.id);
  if (!serviceName) throw new Error("workspace_runtime_service_missing");
  const port = Number(process.env.OPL_WORKSPACE_WEBUI_PORT || 3000);
  return `http://${serviceName}:${port}`;
}

function workspaceGatewayPath(pathname, workspaceId) {
  if (!pathname.startsWith("/w/")) return pathname || "/";
  const prefix = `/w/${workspaceId}`;
  const stripped = pathname.slice(prefix.length);
  return stripped && stripped !== "/" ? stripped : "/";
}

function workspaceGatewayId(url, request) {
  if (url.pathname.startsWith("/w/")) return url.pathname.split("/").filter(Boolean)[1] || "";
  return parseCookies(request.headers.cookie || "")[activeWorkspaceCookieName] || "";
}

function cleanWorkspaceSearch(searchParams) {
  const next = new URLSearchParams(searchParams);
  next.delete("token");
  const value = next.toString();
  return value ? `?${value}` : "";
}

function workspaceUnavailableStatus(workspace) {
  if (workspace.state !== "running") return 409;
  if (workspace.server?.status !== "running") return 409;
  if (workspace.access?.tokenStatus !== "active") return 403;
  return 0;
}

function workspaceErrorText(value) {
  const labels = {
    workspace_not_found: "未找到 OPL Workspace。",
    workspace_token_inactive: "OPL Workspace 访问令牌已失效。",
    workspace_token_invalid: "OPL Workspace 访问令牌无效。",
    workspace_runtime_service_missing: "OPL Workspace 运行时服务未登记。"
  };
  return labels[value] || value;
}

export function hourlyResourceBillingSourceEventId(date = new Date()) {
  const hour = new Date(date);
  hour.setUTCMinutes(0, 0, 0);
  return `resource_billing_tick:${hour.toISOString()}`;
}

function billingWorkerEnabled(env = process.env) {
  const explicit = env.OPL_RESOURCE_BILLING_WORKER_ENABLED;
  if (explicit === "1" || explicit === "true") return true;
  if (explicit === "0" || explicit === "false") return false;
  return env.NODE_ENV === "production";
}

export function startResourceBillingWorker({
  appService = service,
  env = process.env,
  setIntervalFn = setInterval,
  clearIntervalFn = clearInterval,
  nowFn = () => new Date(),
  logger = console
} = {}) {
  if (!billingWorkerEnabled(env)) {
    return { started: false, stop() {}, async tick() {} };
  }
  const intervalMs = Math.max(60_000, Number(env.OPL_RESOURCE_BILLING_INTERVAL_MS || 3_600_000));
  let running = false;
  const tick = async () => {
    if (running) return;
    running = true;
    const sourceEventId = hourlyResourceBillingSourceEventId(nowFn());
    try {
      const result = await appService.settleResourceBilling({ hours: 1, sourceEventId });
      logger.info?.(`OPL resource billing tick settled ${result?.entries?.length || 0} entries for ${sourceEventId}`);
    } catch (error) {
      logger.error?.(`OPL resource billing tick failed for ${sourceEventId}: ${error.message}`);
    } finally {
      running = false;
    }
  };
  const timer = setIntervalFn(tick, intervalMs);
  timer?.unref?.();
  return {
    started: true,
    intervalMs,
    tick,
    stop() {
      clearIntervalFn(timer);
    }
  };
}

async function handleWorkspaceGateway(request, response, url, appService) {
  const parts = url.pathname.split("/").filter(Boolean);
  const workspaceId = workspaceGatewayId(url, request);
  if (!workspaceId) return sendHtml(response, 404, "<!doctype html><title>OPL Workspace 不可用</title><h1>OPL Workspace 不可用</h1>");

  const queryToken = url.searchParams.get("token") || "";
  const cookies = parseCookies(request.headers.cookie || "");
  const token = queryToken || cookies[workspaceAccessCookieName(workspaceId)] || "";
  let workspace;
  try {
    workspace = await appService.resolveWorkspaceAccess({
      workspaceId,
      slug: workspaceId,
      token
    });
  } catch (error) {
    return sendHtml(response, 403, `<!doctype html><title>OPL Workspace 不可用</title><h1>OPL Workspace 不可用</h1><p>${workspaceErrorText(error.message)}</p>`);
  }

  const unavailableStatus = workspaceUnavailableStatus(workspace);
  if (unavailableStatus) {
    return sendHtml(response, unavailableStatus, workspaceUnavailableHtml());
  }

  const setCookie = queryToken ? workspaceAccessCookies({ workspaceId, token: queryToken }) : null;
  const isWorkspaceEntryPath = url.pathname.startsWith("/w/") && parts.length === 2;
  if ((request.method === "GET" || request.method === "HEAD") && isWorkspaceEntryPath && !url.pathname.endsWith("/")) {
    response.writeHead(308, {
      location: `${url.pathname}/${url.search}`,
      ...(setCookie ? { "set-cookie": setCookie } : {})
    });
    return response.end();
  }

  try {
    const upstreamPath = workspaceGatewayPath(url.pathname, workspaceId);
    const upstreamUrl = new URL(`${upstreamPath}${cleanWorkspaceSearch(url.searchParams)}`, workspaceRuntimeBaseUrl(workspace));
    const init = {
      method: request.method,
      headers: proxyRequestHeaders(request),
      redirect: "manual"
    };
    if (request.method !== "GET" && request.method !== "HEAD") {
      init.body = request;
      init.duplex = "half";
    }
    const upstream = await fetch(upstreamUrl, init);
    const headers = proxyResponseHeaders(upstream);
    appendSetCookie(headers, setCookie);
    if (shouldBufferWorkspaceResponse({ request, upstreamPath })) {
      const body = Buffer.from(await upstream.arrayBuffer());
      const responseBody = workspaceStaticBody({ headers, body });
      response.writeHead(upstream.status, headers);
      response.end(responseBody);
      return;
    }
    response.writeHead(upstream.status, headers);
    if (request.method === "HEAD" || !upstream.body) return response.end();
    for await (const chunk of upstream.body) {
      response.write(Buffer.from(chunk));
    }
    response.end();
  } catch (error) {
    return sendHtml(response, 502, workspaceUnavailableHtml({
      title: "OPL Workspace 网关不可用",
      heading: "工作区已释放或运行时不可用",
      message: "返回控制台检查资源状态，或带上 Workspace ID 提交工单。"
    }));
  }
}

function isWorkspaceBackendPath(pathname) {
  return pathname.startsWith("/api/") || pathname === "/ws" || pathname.startsWith("/ws/") || pathname.startsWith("/assets/");
}

function shouldProxyWorkspaceBackend(request, url) {
  if (!isWorkspaceBackendPath(url.pathname)) return false;
  return Boolean(parseCookies(request.headers.cookie || "")[activeWorkspaceCookieName]);
}

function writeUpgradeError(socket, status, message) {
  socket.write(`HTTP/1.1 ${status} ${message}\r\nConnection: close\r\nContent-Length: 0\r\n\r\n`);
  socket.destroy();
}

function destroySocketQuietly(socket) {
  if (!socket?.destroyed) socket.destroy();
}

function upgradeHeaders(request, target) {
  const headers = [];
  for (const [name, value] of headerEntries(request.headers)) {
    const key = name.toLowerCase();
    if (key === "host") {
      headers.push(`Host: ${target.host}`);
      continue;
    }
    if (key === "cookie") {
      const runtimeCookies = runtimeCookieHeader(value);
      if (runtimeCookies) headers.push(`Cookie: ${runtimeCookies}`);
      continue;
    }
    headers.push(`${name}: ${value}`);
  }
  return headers;
}

export function createUpgradeHandler({ appService = service } = {}) {
  return async (request, socket, head) => {
    const url = new URL(request.url, "http://localhost");
    if (!shouldProxyWorkspaceBackend(request, url)) return writeUpgradeError(socket, 404, "Not Found");
    const workspaceId = workspaceGatewayId(url, request);
    const cookies = parseCookies(request.headers.cookie || "");
    const token = cookies[workspaceAccessCookieName(workspaceId)] || "";
    try {
      const workspace = await appService.resolveWorkspaceAccess({ workspaceId, slug: workspaceId, token });
      const unavailableStatus = workspaceUnavailableStatus(workspace);
      if (unavailableStatus) return writeUpgradeError(socket, unavailableStatus, "Workspace Unavailable");
      const target = new URL(workspaceRuntimeBaseUrl(workspace));
      const upstream = connect(Number(target.port || 80), target.hostname);
      let tunnelEstablished = false;
      socket.on("error", () => destroySocketQuietly(upstream));
      socket.on("close", () => destroySocketQuietly(upstream));
      upstream.on("connect", () => {
        tunnelEstablished = true;
        upstream.write(`${request.method} ${url.pathname}${url.search} HTTP/${request.httpVersion}\r\n`);
        upstream.write(`${upgradeHeaders(request, target).join("\r\n")}\r\n\r\n`);
        if (head?.length) upstream.write(head);
        socket.pipe(upstream).pipe(socket);
      });
      upstream.on("error", () => {
        if (!tunnelEstablished && !socket.destroyed) return writeUpgradeError(socket, 502, "Bad Gateway");
        destroySocketQuietly(socket);
      });
      upstream.on("close", () => destroySocketQuietly(socket));
    } catch {
      writeUpgradeError(socket, 403, "Forbidden");
    }
  };
}

function errorStatus(error) {
  return Number.isInteger(error.status) ? error.status : 400;
}

function scopedAccountId(auth, session, requestedAccountId) {
  return auth ? auth.accountIdFor(session.user, requestedAccountId) : requestedAccountId || null;
}

function scopedWorkspaceInput(auth, session, body) {
  return auth ? auth.workspaceInputFor(session.user, body) : body;
}

function requireAdmin(auth, session) {
  if (auth) auth.requireAdmin(session.user);
}

function apiRouteKey(method, pathname) {
  const computeAllocationDetail = pathname.match(/^\/api\/compute-allocations\/([^/]+)$/);
  if (method === "GET" && computeAllocationDetail) {
    return {
      routeKey: "GET /api/compute-allocations/:id",
      pathParams: { id: decodeURIComponent(computeAllocationDetail[1]) }
    };
  }
  const computeAllocationDestroy = pathname.match(/^\/api\/compute-allocations\/([^/]+)\/destroy$/);
  if (method === "POST" && computeAllocationDestroy) {
    return {
      routeKey: "POST /api/compute-allocations/:id/destroy",
      pathParams: { id: decodeURIComponent(computeAllocationDestroy[1]) }
    };
  }
  return { routeKey: `${method} ${pathname}`, pathParams: {} };
}

async function handleApi(request, response, pathname, appService, operatorSummaryToken = process.env.OPL_OPERATOR_SUMMARY_TOKEN, auth = null) {
  try {
    const { routeKey, pathParams } = apiRouteKey(request.method, pathname);
    const publicRoutes = buildApiRoutes({
      appService,
      auth,
      request,
      response,
      readJson,
      body: {},
      pathParams,
      operatorSummaryToken,
      session: null,
      isAdminSession: false,
      scopedAccountId: (requestedAccountId) => scopedAccountId(auth, null, requestedAccountId),
      scopedWorkspaceInput: (body) => scopedWorkspaceInput(auth, null, body),
      requireAdmin: () => requireAdmin(auth, null)
    });
    if (publicApiRoutes.has(routeKey) || pathname.startsWith("/api/auth/")) {
      const handler = publicRoutes[routeKey];
      if (!handler) return sendJson(response, 404, { ok: false, error: "route_not_found" });
      return sendJson(response, 200, await handler());
    }
    if (!auth && pathname.startsWith("/api/auth/")) {
      return sendJson(response, 404, { ok: false, error: "route_not_found" });
    }

    const session = auth
      ? await auth.requireSession(request, { requireCsrf: request.method !== "GET" && request.method !== "HEAD" })
      : null;

    const body = request.method === "GET" || request.method === "HEAD" ? {} : await readJson(request);
    const isAdminSession = Boolean(auth && auth.isAdmin(session.user));
    const routes = buildApiRoutes({
      appService,
      auth,
      request,
      response,
      readJson,
      body,
      pathParams,
      operatorSummaryToken,
      session,
      isAdminSession,
      scopedAccountId: (requestedAccountId) => scopedAccountId(auth, session, requestedAccountId),
      scopedWorkspaceInput: (input) => scopedWorkspaceInput(auth, session, input),
      requireAdmin: () => requireAdmin(auth, session)
    });
    const handler = routes[routeKey];
    if (!handler) return sendJson(response, 404, { ok: false, error: "route_not_found" });
    return sendJson(response, 200, await handler());
  } catch (error) {
    return sendJson(response, errorStatus(error), errorPayload(error));
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
    if (!extname(pathname)) {
      try {
        const content = await readFile(join(staticDir, "index.html"));
        response.writeHead(200, { "content-type": contentTypes[".html"] });
        response.end(content);
        return;
      } catch {
        // fall through to plain 404
      }
    }
    response.writeHead(404, { "content-type": "text/plain; charset=utf-8" });
    response.end("未找到\n");
  }
}

export function createRequestHandler({ appService = service, staticDir = publicDir, operatorSummaryToken = process.env.OPL_OPERATOR_SUMMARY_TOKEN, auth = appService === service ? defaultAuth : null } = {}) {
  return (request, response) => {
    const url = new URL(request.url, "http://localhost");
    if (shouldProxyWorkspaceBackend(request, url)) return handleWorkspaceGateway(request, response, url, appService);
    if (url.pathname.startsWith("/api/")) return handleApi(request, response, url.pathname, appService, operatorSummaryToken, auth);
    if (url.pathname.startsWith("/w/")) return handleWorkspaceGateway(request, response, url, appService);
    if (url.pathname.startsWith("/workspaces/")) {
      return handleWorkspaceUrl(request, response, url.pathname, url.searchParams, appService);
    }
    return serveStatic(response, url.pathname, staticDir);
  };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const server = createServer(createRequestHandler());
  server.on("upgrade", createUpgradeHandler());
  const billingWorker = startResourceBillingWorker();
  server.listen(port, () => {
    console.log(`OPL Cloud API listening on http://127.0.0.1:${port}`);
    console.log(`State file: ${dataPath}`);
  });
  server.on("close", () => billingWorker.stop());
}
