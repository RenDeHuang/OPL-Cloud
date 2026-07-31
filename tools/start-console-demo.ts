import { createServer as createHttpServer } from "node:http";
import { randomBytes } from "node:crypto";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { createServer as createViteServer } from "vite";

import {
  apiFixture,
  CONSOLE_DEMO_CREDENTIALS,
  createConsoleFixtureSession,
  createConsoleFixtureState
} from "./console-browser-qa.ts";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const HOST = "127.0.0.1";
const SESSION_COOKIE = "opl_console_demo_session";
const API_PATH_PATTERN = /^\/api(?:$|[/?#]|[^/])/;

export { CONSOLE_DEMO_CREDENTIALS };

function requestHeaders(request) {
  return Object.fromEntries(Object.entries(request.headers).map(([name, value]) => [
    name.toLowerCase(),
    Array.isArray(value) ? value.join(", ") : value || ""
  ]));
}

async function requestBody(request) {
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > 1_000_000) throw new Error("console_demo_request_too_large");
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString("utf8");
}

function parseCookies(header = "") {
  return Object.fromEntries(header.split(";").map((item) => item.trim()).filter(Boolean).map((item) => {
    const separator = item.indexOf("=");
    return separator < 0 ? [item, ""] : [item.slice(0, separator), item.slice(separator + 1)];
  }));
}

function sessionCookie(value, maxAge = undefined) {
  const attributes = [`${SESSION_COOKIE}=${value}`, "Path=/", "HttpOnly", "SameSite=Strict"];
  if (maxAge !== undefined) attributes.push(`Max-Age=${maxAge}`);
  return attributes.join("; ");
}

function fixtureRoute(request, response, body, sessionHeaders = {}) {
  const headers = requestHeaders(request);
  const url = new URL(request.url || "/", `http://${HOST}`);
  let settled = false;
  return {
    request: () => ({
      url: () => url.href,
      method: () => request.method || "GET",
      headers: () => headers,
      postDataJSON: () => body ? JSON.parse(body) : {}
    }),
    fulfill: async ({ status = 200, contentType = "application/json", headers: outputHeaders = {}, body: outputBody = "" } = {}) => {
      if (settled) return;
      settled = true;
      response.statusCode = status;
      response.setHeader("content-type", contentType);
      for (const [name, value] of Object.entries({ ...outputHeaders, ...sessionHeaders })) response.setHeader(name, value);
      response.end(outputBody);
    },
    abort: async () => {
      if (settled) return;
      settled = true;
      response.statusCode = 503;
      response.setHeader("content-type", "application/json");
      response.end(JSON.stringify({ error: "console_demo_fixture_abort" }));
    }
  };
}

export async function startConsoleDemoServer({ port = 5197, log = true } = {}) {
  if (!Number.isSafeInteger(port) || port < 0 || port > 65_535) throw new Error("console_demo_port_invalid");

  const state = createConsoleFixtureState({ faultInjection: false, seedDemoData: true });
  const sessions = new Map();
  const vite = await createViteServer({
    root: ROOT,
    configFile: resolve(ROOT, "vite.config.ts"),
    appType: "spa",
    logLevel: log ? "info" : "silent",
    server: { middlewareMode: true, hmr: false, proxy: {} }
  });

  const server = createHttpServer(async (request, response) => {
    try {
      const rawUrl = request.url || "/";
      if (API_PATH_PATTERN.test(rawUrl)) {
        const parsedUrl = new URL(rawUrl, `http://${HOST}`);
        if (!parsedUrl.pathname.startsWith("/api/")) {
          response.statusCode = 404;
          response.setHeader("content-type", "application/json");
          response.end(JSON.stringify({ error: "console_demo_api_not_found" }));
          return;
        }
        const body = await requestBody(request);
        const cookies = parseCookies(request.headers.cookie);
        const existingSessionId = cookies[SESSION_COOKIE] || "";
        const isLogin = parsedUrl.pathname === "/api/auth/login" && request.method === "POST";
        const isLogout = parsedUrl.pathname === "/api/auth/logout" && request.method === "POST";
        const sessionId = isLogin ? randomBytes(24).toString("base64url") : existingSessionId;
        const session = isLogin
          ? createConsoleFixtureSession()
          : sessions.get(sessionId) || createConsoleFixtureSession();
        if (isLogin && existingSessionId) sessions.delete(existingSessionId);
        const sessionHeaders = isLogin
          ? { "set-cookie": sessionCookie(sessionId) }
          : isLogout
            ? { "set-cookie": sessionCookie("", 0) }
            : {};
        await apiFixture(fixtureRoute(request, response, body, sessionHeaders), state, session);
        if (isLogin && session.authenticated) sessions.set(sessionId, session);
        if (isLogout && sessionId) sessions.delete(sessionId);
        return;
      }
      vite.middlewares(request, response, (error) => {
        if (!error || response.headersSent) return;
        response.statusCode = 500;
        response.setHeader("content-type", "application/json");
        response.end(JSON.stringify({ error: "console_demo_ui_failed" }));
      });
    } catch (error) {
      if (response.headersSent) return;
      response.statusCode = error instanceof SyntaxError ? 400 : 500;
      response.setHeader("content-type", "application/json");
      response.end(JSON.stringify({ error: error instanceof Error ? error.message : "console_demo_request_failed" }));
    }
  });

  try {
    await new Promise((resolveListen, rejectListen) => {
      server.once("error", rejectListen);
      server.listen(port, HOST, () => {
        server.off("error", rejectListen);
        resolveListen();
      });
    });
  } catch (error) {
    await vite.close();
    throw error;
  }

  const address = server.address();
  if (!address || typeof address === "string") {
    server.close();
    await vite.close();
    throw new Error("console_demo_address_missing");
  }
  const origin = `http://${HOST}:${address.port}`;
  if (log) {
    process.stdout.write([
      "OPL Console localhost demo",
      `URL: ${origin}/login`,
      `Customer: ${CONSOLE_DEMO_CREDENTIALS.customer.email} / ${CONSOLE_DEMO_CREDENTIALS.customer.password}`,
      `Admin: ${CONSOLE_DEMO_CREDENTIALS.admin.email} / ${CONSOLE_DEMO_CREDENTIALS.admin.password}`,
      "Data: fake-only, in-memory, no external network or real billing/resource mutations",
      ""
    ].join("\n"));
  }

  let closed = false;
  return {
    origin,
    state,
    async close() {
      if (closed) return;
      closed = true;
      const closedServer = new Promise((resolveClose, rejectClose) => {
        server.close((error) => error ? rejectClose(error) : resolveClose());
      });
      server.closeIdleConnections();
      const forceClose = setTimeout(() => server.closeAllConnections(), 250);
      forceClose.unref();
      try {
        await closedServer;
      } finally {
        clearTimeout(forceClose);
        server.closeAllConnections();
        sessions.clear();
        await vite.close();
      }
    }
  };
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  const port = Number(process.env.PORT || 5197);
  startConsoleDemoServer({ port })
    .then((demo) => {
      const close = async () => {
        await demo.close();
        process.exit(0);
      };
      process.once("SIGINT", () => { void close(); });
      process.once("SIGTERM", () => { void close(); });
    })
    .catch((error) => {
      process.stderr.write(`${JSON.stringify({ ok: false, error: error instanceof Error ? error.message : String(error) }, null, 2)}\n`);
      process.exitCode = 1;
    });
}
