import { createServer } from "node:http";
import { randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { createOplCloud } from "./src/opl-cloud.js";
import { productionReadiness } from "./src/production-readiness.js";
import { createRuntimeProvider } from "./src/runtime-provider-factory.js";
import { JsonFileStore, PostgresStore } from "./src/store.js";

const root = fileURLToPath(new URL("../..", import.meta.url));
const publicDir = join(root, "dist");
const port = Number(process.env.PORT ?? 8787);
const dataPath = process.env.OPL_CLOUD_DATA_PATH ?? join(root, ".runtime", "opl-cloud-state.json");
const sessionCookieName = "opl_console_session";

function commercialFlag(env, name) {
  return env[name] !== "0";
}

class ApiError extends Error {
  constructor(message, statusCode = 400) {
    super(message);
    this.statusCode = statusCode;
  }
}

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

function sendJson(response, status, payload, headers = {}) {
  response.writeHead(status, { "content-type": "application/json; charset=utf-8", ...headers });
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

function parseCookies(cookieHeader = "") {
  return Object.fromEntries(
    String(cookieHeader)
      .split(";")
      .map((part) => part.trim())
      .filter(Boolean)
      .map((part) => {
        const separator = part.indexOf("=");
        if (separator === -1) return [part, ""];
        return [part.slice(0, separator), decodeURIComponent(part.slice(separator + 1))];
      })
  );
}

function sessionIdFromRequest(request) {
  return parseCookies(request.headers.cookie || "")[sessionCookieName] || "";
}

function sessionCookie(sessionId, { secure = false } = {}) {
  return {
    "set-cookie": `${sessionCookieName}=${encodeURIComponent(sessionId)}; Path=/; HttpOnly; SameSite=Lax; Max-Age=604800${secure ? "; Secure" : ""}`
  };
}

function clearSessionCookie({ secure = false } = {}) {
  return {
    "set-cookie": `${sessionCookieName}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0; Expires=Thu, 01 Jan 1970 00:00:00 GMT${secure ? "; Secure" : ""}`
  };
}

function publicSessionPayload(session) {
  const { csrfToken, userId, userEmail, ...payload } = session || {};
  return payload;
}

function csrfTokenFromRequest(request) {
  return request.headers["x-opl-csrf-token"] || "";
}

function authHeaders(sessionId, session, { cookieSecure = false } = {}) {
  return {
    ...sessionCookie(sessionId, { secure: cookieSecure }),
    ...(session?.csrfToken ? { "x-opl-csrf-token": session.csrfToken } : {})
  };
}

function statusForError(error) {
  if (error.statusCode) return error.statusCode;
  if (error.message === "invalid_credentials") return 401;
  if (error.message === "authentication_required") return 401;
  if (error.message === "user_disabled") return 403;
  if (error.message === "operator_role_required") return 403;
  if (error.message === "account_scope_mismatch") return 403;
  if (error.message === "csrf_required") return 403;
  return 400;
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
      <p>Workspace link is valid. The Workspace is ${isRunning ? "ready" : "not ready yet"}; retry shortly if access does not open automatically.</p>
      <dl>
        <dt>Status</dt><dd>${isRunning ? "Ready" : "Preparing"}</dd>
        <dt>Compute</dt><dd>${workspace.server.status === "running" ? "Running" : "Preparing"}</dd>
        <dt>Storage</dt><dd>${workspace.disk.status === "destroyed" ? "Unavailable" : "Ready"}</dd>
        <dt>Access</dt><dd>${workspace.access.tokenStatus === "active" ? "Active" : "Unavailable"}</dd>
      </dl>
    </main>
  </body>
</html>`);
  } catch (error) {
    return sendHtml(response, 403, `<!doctype html><title>OPL Workspace unavailable</title><h1>OPL Workspace unavailable</h1><p>${error.message}</p>`);
  }
}

async function currentSession(appService, request, enforceSessionScope) {
  const sessionId = sessionIdFromRequest(request);
  if (typeof appService.getConsoleSession === "function") {
    try {
      if (sessionId) {
        const session = await appService.getConsoleSession(sessionId, { includeSecurity: true });
        if (session) return session;
      }
    } catch (error) {
      if (error.message === "user_disabled") throw new ApiError("user_disabled", 401);
      throw error;
    }
    if (sessionId) {
      const session = await appService.getConsoleSession(sessionId, { includeSecurity: true });
      if (session) return session;
    }
    if (enforceSessionScope) throw new ApiError("authentication_required", 401);
    return appService.getConsoleSession("", { includeSecurity: true });
  }
  if (enforceSessionScope && !sessionId) throw new ApiError("authentication_required", 401);
  return {
    accountId: "pi-alpha",
    tenantId: "default",
    displayName: "pi-alpha",
    email: "",
    role: "pi"
  };
}

async function scopedBody({ appService, request, body, pathname, enforceSessionScope, session = null }) {
  if (!enforceSessionScope) return body;
  const current = session || await currentSession(appService, request, enforceSessionScope);
  const operator = current.role === "operator";

  if (pathname === "/api/accounts/credit" || pathname === "/api/workspaces/runtime-status") {
    if (!operator) throw new Error("operator_role_required");
  }
  if (body.accountId && body.accountId !== current.accountId && !operator) {
    throw new Error("account_scope_mismatch");
  }
  if (!operator) return { ...body, accountId: current.accountId };
  return body;
}

async function csrfProtectedSession({ appService, request, enforceSessionScope, enforceCsrf }) {
  const session = await currentSession(appService, request, enforceSessionScope);
  if (!enforceCsrf) return session;
  if (!session.csrfToken || csrfTokenFromRequest(request) !== session.csrfToken) {
    throw new ApiError("csrf_required", 403);
  }
  return session;
}

async function handleApi(
  request,
  response,
  pathname,
  appService,
  operatorSummaryToken = process.env.OPL_OPERATOR_SUMMARY_TOKEN,
  enforceSessionScope = commercialFlag(process.env, "OPL_ENFORCE_SESSION_SCOPE"),
  consoleAccessToken = process.env.OPL_CONSOLE_ACCESS_TOKEN,
  enforceCsrf = commercialFlag(process.env, "OPL_ENFORCE_CSRF"),
  cookieSecure = process.env.OPL_COOKIE_SECURE === "1"
) {
  try {
    if (request.method === "GET" && pathname === "/api/session") {
      const session = await currentSession(appService, request, enforceSessionScope);
      return sendJson(response, 200, publicSessionPayload(session), session.csrfToken ? { "x-opl-csrf-token": session.csrfToken } : {});
    }
    if (request.method === "GET" && pathname === "/api/state") {
      const url = new URL(request.url, "http://localhost");
      const session = await currentSession(appService, request, enforceSessionScope);
      const accountId = url.searchParams.get("accountId") ?? session.accountId;
      if (enforceSessionScope && session.role !== "operator" && accountId !== session.accountId) {
        throw new ApiError("account_scope_mismatch", 403);
      }
      return sendJson(response, 200, await appService.getState(accountId, {
        includeOperatorEvidence: session.role === "operator"
      }));
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
    if (request.method === "POST" && pathname === "/api/auth/operator-login") {
      if (!operatorSummaryToken || body.operatorToken !== operatorSummaryToken) {
        throw new ApiError("operator_token_required", 403);
      }
      const nextSessionId = sessionIdFromRequest(request) || randomUUID();
      const { operatorToken, ...loginInput } = body;
      const session = await appService.operatorLogin(loginInput, { sessionId: nextSessionId });
      return sendJson(response, 200, session, authHeaders(nextSessionId, session, { cookieSecure }));
    }
    if (request.method === "POST" && pathname === "/api/auth/accept-invite") {
      const nextSessionId = sessionIdFromRequest(request) || randomUUID();
      const session = await appService.acceptInvite(body, { sessionId: nextSessionId });
      return sendJson(response, 200, session, authHeaders(nextSessionId, session, { cookieSecure }));
    }
    if (request.method === "POST" && pathname === "/api/auth/login") {
      const nextSessionId = sessionIdFromRequest(request) || randomUUID();
      const session = await appService.loginUser(body, { sessionId: nextSessionId });
      return sendJson(response, 200, session, authHeaders(nextSessionId, session, { cookieSecure }));
    }
    if (request.method === "POST" && pathname === "/api/auth/logout") {
      await csrfProtectedSession({ appService, request, enforceSessionScope, enforceCsrf });
      const existingSessionId = sessionIdFromRequest(request);
      if (typeof appService.deleteConsoleSession === "function") {
        await appService.deleteConsoleSession(existingSessionId);
      }
      return sendJson(response, 200, { ok: true }, clearSessionCookie({ secure: cookieSecure }));
    }
    if (request.method === "GET" && pathname === "/api/operator/users") {
      const session = await currentSession(appService, request, enforceSessionScope);
      if (session.role !== "operator") throw new ApiError("operator_role_required", 403);
      return sendJson(response, 200, await appService.listIdentityUsers({ actor: session }));
    }
    if (request.method === "POST" && pathname === "/api/operator/users") {
      const session = await csrfProtectedSession({ appService, request, enforceSessionScope, enforceCsrf });
      if (session.role !== "operator") throw new ApiError("operator_role_required", 403);
      return sendJson(response, 200, await appService.createIdentityUser({ actor: session, ...body }));
    }
    if (request.method === "POST" && pathname === "/api/operator/users/invite") {
      const session = await csrfProtectedSession({ appService, request, enforceSessionScope, enforceCsrf });
      if (session.role !== "operator") throw new ApiError("operator_role_required", 403);
      return sendJson(response, 200, await appService.inviteUser({ actor: session, ...body }));
    }
    if (request.method === "POST" && pathname === "/api/operator/users/disable") {
      const session = await csrfProtectedSession({ appService, request, enforceSessionScope, enforceCsrf });
      if (session.role !== "operator") throw new ApiError("operator_role_required", 403);
      return sendJson(response, 200, await appService.disableUser({ actor: session, ...body }));
    }
    if (request.method === "POST" && pathname === "/api/session") {
      const existingSessionId = sessionIdFromRequest(request);
      const existingSession = existingSessionId && typeof appService.getConsoleSession === "function"
        ? await appService.getConsoleSession(existingSessionId, { includeSecurity: true })
        : null;
      const nextSessionId = existingSessionId || randomUUID();

      if (body.role === "operator" && (!operatorSummaryToken || body.operatorToken !== operatorSummaryToken)) {
        throw new ApiError("operator_token_required", 403);
      }
      if (consoleAccessToken && !existingSession && body.role !== "operator" && body.accessToken !== consoleAccessToken) {
        throw new ApiError("console_access_token_required", 401);
      }
      const { operatorToken, accessToken, ...sessionInput } = body;
      if (enforceSessionScope && existingSession?.role !== "operator" && existingSession?.accountId) {
        if (sessionInput.accountId && sessionInput.accountId !== existingSession.accountId) {
          throw new ApiError("account_scope_mismatch", 403);
        }
        sessionInput.accountId = existingSession.accountId;
        sessionInput.tenantId = existingSession.tenantId;
        sessionInput.role = existingSession.role;
      }
      const session = await appService.updateConsoleSession(sessionInput, { sessionId: nextSessionId });
      const sessionWithSecurity = await appService.getConsoleSession(nextSessionId, { includeSecurity: true });
      return sendJson(response, 200, session, authHeaders(nextSessionId, sessionWithSecurity, { cookieSecure }));
    }
    const protectedSession = request.method === "POST"
      ? await csrfProtectedSession({ appService, request, enforceSessionScope, enforceCsrf })
      : null;
    const accountScopedBody = await scopedBody({ appService, request, body, pathname, enforceSessionScope, session: protectedSession });
    const routes = {
      "POST /api/accounts/credit": () => appService.creditAccount(accountScopedBody),
      "POST /api/workspaces": () => appService.createWorkspace(accountScopedBody),
      "POST /api/workspaces/stop-server": () => appService.stopServer(accountScopedBody),
      "POST /api/workspaces/restart-server": () => appService.restartServer(accountScopedBody),
      "POST /api/workspaces/destroy-server": () => appService.destroyServer(accountScopedBody),
      "POST /api/workspaces/destroy-disk": () => appService.destroyDisk(accountScopedBody),
      "POST /api/workspaces/runtime-status": () => appService.runtimeStatus(accountScopedBody),
      "POST /api/workspaces/reset-token": () => appService.resetWorkspaceToken(accountScopedBody),
      "POST /api/workspaces/delete-token": () => appService.deleteWorkspaceToken(accountScopedBody),
      "POST /api/billing/settle": () => appService.settleBilling(accountScopedBody)
    };
    const handler = routes[`${request.method} ${pathname}`];
    if (!handler) return sendJson(response, 404, { ok: false, error: "route_not_found" });
    return sendJson(response, 200, await handler());
  } catch (error) {
    return sendJson(response, statusForError(error), { ok: false, error: error.message });
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
    if (!extname(fullPath)) {
      try {
        const content = await readFile(join(staticDir, "index.html"));
        response.writeHead(200, { "content-type": contentTypes[".html"] });
        response.end(content);
        return;
      } catch {
        // Fall through to the normal 404 when the app shell is not available.
      }
    }
    response.writeHead(404, { "content-type": "text/plain; charset=utf-8" });
    response.end("Not found\n");
  }
}

export function createRequestHandler({
  appService = service,
  staticDir = publicDir,
  operatorSummaryToken = process.env.OPL_OPERATOR_SUMMARY_TOKEN,
  enforceSessionScope = commercialFlag(process.env, "OPL_ENFORCE_SESSION_SCOPE"),
  consoleAccessToken = process.env.OPL_CONSOLE_ACCESS_TOKEN,
  enforceCsrf = commercialFlag(process.env, "OPL_ENFORCE_CSRF"),
  cookieSecure = process.env.OPL_COOKIE_SECURE === "1"
} = {}) {
  return (request, response) => {
    const url = new URL(request.url, "http://localhost");
    if (url.pathname.startsWith("/api/")) return handleApi(request, response, url.pathname, appService, operatorSummaryToken, enforceSessionScope, consoleAccessToken, enforceCsrf, cookieSecure);
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
