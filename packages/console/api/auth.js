import { randomBytes } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { hashPassword, normalizeEmail, verifyPassword } from "../src/auth-credentials.js";

const root = fileURLToPath(new URL("../../..", import.meta.url));
const defaultUsersPath = join(root, ".runtime", "opl-console-users.json");
const sessionCookieName = "opl_console_session";
const defaultSessionTtlMs = 24 * 60 * 60 * 1000;
function authError(status, message) {
  const error = new Error(message);
  error.status = status;
  return error;
}

function publicUser(user) {
  return {
    id: user.id,
    email: user.email,
    name: user.name || "",
    role: user.role || "pi",
    accountId: user.accountId,
    organizationId: user.organizationId || null,
    status: user.status || "active"
  };
}

function now() {
  return new Date().toISOString();
}

function isAuthUser(user) {
  return Boolean(user?.id && user.email && user.accountId && user.passwordHash);
}

function normalizeStoredAuthUser(user) {
  return {
    ...user,
    email: normalizeEmail(user.email),
    name: user.name || "",
    role: user.role || "pi",
    organizationId: user.organizationId || null,
    status: user.status || "active"
  };
}

function authUsersFromState(state) {
  return Object.values(state.users || {})
    .filter(isAuthUser)
    .map(normalizeStoredAuthUser);
}

function parseCookies(header = "") {
  return String(header)
    .split(";")
    .map((part) => part.trim())
    .filter(Boolean)
    .reduce((cookies, part) => {
      const index = part.indexOf("=");
      if (index === -1) return cookies;
      cookies[decodeURIComponent(part.slice(0, index))] = decodeURIComponent(part.slice(index + 1));
      return cookies;
    }, {});
}

function secureCookieFor(request, env) {
  if (env.OPL_CONSOLE_COOKIE_SECURE === "1") return true;
  if (env.OPL_CONSOLE_COOKIE_SECURE === "0") return false;
  return request.headers["x-forwarded-proto"] === "https" || env.NODE_ENV === "production";
}

function serializeSessionCookie(value, { request, env, maxAgeSeconds }) {
  const parts = [
    `${sessionCookieName}=${encodeURIComponent(value)}`,
    "Path=/",
    "HttpOnly",
    "SameSite=Lax",
    `Max-Age=${maxAgeSeconds}`
  ];
  if (secureCookieFor(request, env)) parts.push("Secure");
  return parts.join("; ");
}

function randomToken(bytes = 32) {
  return randomBytes(bytes).toString("base64url");
}

function stableIdPart(value) {
  return String(value || "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "") || "user";
}

function envSeedUser(env, { prefix, role, fallbackName }) {
  const userId = env[`${prefix}_USER_ID`];
  const email = env[`${prefix}_EMAIL`];
  const password = env[`${prefix}_PASSWORD`];
  const passwordHash = env[`${prefix}_PASSWORD_HASH`];
  const accountId = env[`${prefix}_ACCOUNT_ID`];
  const name = env[`${prefix}_NAME`];
  const hasAnyField = [userId, email, password, passwordHash, accountId, name].some(Boolean);
  if (!hasAnyField) return null;
  return {
    id: userId || `usr-${role}-${stableIdPart(accountId || email)}`,
    email,
    password,
    passwordHash,
    name: name || fallbackName,
    role,
    accountId
  };
}

function defaultSeedUsers(env) {
  if (env.OPL_CONSOLE_USERS_JSON) return JSON.parse(env.OPL_CONSOLE_USERS_JSON);
  return [
    envSeedUser(env, { prefix: "OPL_PI", role: "pi", fallbackName: "Lab Owner" }),
    envSeedUser(env, { prefix: "OPL_ADMIN", role: "admin", fallbackName: "OPL Admin" })
  ].filter(Boolean);
}

async function normalizeSeedUser(user) {
  if (!user.id) throw new Error("auth_user_id_required");
  if (!user.email) throw new Error("auth_user_email_required");
  if (!user.accountId) throw new Error("auth_user_account_required");
  if (!user.passwordHash && !user.password) throw new Error("auth_user_password_required");
  return {
    id: user.id,
    email: normalizeEmail(user.email),
    name: user.name || "",
    role: user.role || "pi",
    accountId: user.accountId,
    organizationId: user.organizationId || null,
    status: user.status || "active",
    passwordHash: user.passwordHash || await hashPassword(user.password)
  };
}

async function readUsers(usersPath) {
  const raw = await readFile(usersPath, "utf8");
  const parsed = JSON.parse(raw);
  const users = Array.isArray(parsed) ? parsed : parsed.users || [];
  return Promise.all(users.map(normalizeSeedUser));
}

async function writeUsers(usersPath, users) {
  await mkdir(dirname(usersPath), { recursive: true });
  await writeFile(usersPath, `${JSON.stringify({ users }, null, 2)}\n`);
}

export function createAuthController({
  env = process.env,
  usersPath = env.OPL_CONSOLE_USERS_PATH || defaultUsersPath,
  seedUsers = null,
  sessionTtlMs = defaultSessionTtlMs,
  store = null
} = {}) {
  const sessions = new Map();
  let cachedUsers = null;

  async function loadStoreUsers() {
    const state = await store.read();
    const existing = authUsersFromState(state);
    if (existing.length > 0) return existing;

    const usersToSeed = await Promise.all((seedUsers || defaultSeedUsers(env)).map(normalizeSeedUser));

    return store.update((nextState) => {
      const current = authUsersFromState(nextState);
      if (current.length > 0) return current;

      nextState.users ??= {};
      for (const user of usersToSeed) {
        const timestamp = now();
        nextState.users[user.id] = {
          ...nextState.users[user.id],
          ...user,
          balance: Number(nextState.users[user.id]?.balance ?? user.balance ?? 0),
          frozen: Number(nextState.users[user.id]?.frozen ?? user.frozen ?? 0),
          holds: nextState.users[user.id]?.holds || user.holds || {},
          totalRecharged: Number(nextState.users[user.id]?.totalRecharged ?? user.totalRecharged ?? 0),
          createdAt: nextState.users[user.id]?.createdAt || timestamp,
          updatedAt: timestamp
        };
      }
      return authUsersFromState(nextState);
    });
  }

  async function loadUsers() {
    if (store) return loadStoreUsers();
    if (cachedUsers) return cachedUsers;
    try {
      cachedUsers = await readUsers(usersPath);
      return cachedUsers;
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
      cachedUsers = await Promise.all((seedUsers || defaultSeedUsers(env)).map(normalizeSeedUser));
      await writeUsers(usersPath, cachedUsers);
      return cachedUsers;
    }
  }

  function setSessionCookie(response, request, sessionId, maxAgeSeconds) {
    response.setHeader("set-cookie", serializeSessionCookie(sessionId, { request, env, maxAgeSeconds }));
  }

  async function sessionFromRequest(request) {
    const sessionId = parseCookies(request.headers.cookie || "")[sessionCookieName];
    if (!sessionId) throw authError(401, "not_authenticated");
    const session = sessions.get(sessionId);
    if (!session || session.expiresAt <= Date.now()) {
      sessions.delete(sessionId);
      throw authError(401, "not_authenticated");
    }
    const users = await loadUsers();
    const user = users.find((item) => item.id === session.userId && item.status !== "disabled");
    if (!user) {
      sessions.delete(sessionId);
      throw authError(401, "not_authenticated");
    }
    return { sessionId, session, user };
  }

  return {
    async login({ email, password }, { request, response }) {
      const users = await loadUsers();
      const user = users.find((item) => item.email === normalizeEmail(email) && item.status !== "disabled");
      if (!user || !await verifyPassword(password, user.passwordHash)) throw authError(401, "invalid_credentials");
      const sessionId = randomToken();
      const csrfToken = randomToken(24);
      sessions.set(sessionId, {
        userId: user.id,
        csrfToken,
        createdAt: new Date().toISOString(),
        expiresAt: Date.now() + sessionTtlMs
      });
      setSessionCookie(response, request, sessionId, Math.floor(sessionTtlMs / 1000));
      return { user: publicUser(user), csrfToken };
    },

    async operatorLogin({ operatorToken }, { request, response, expectedToken }) {
      if (!expectedToken) throw authError(403, "operator_token_not_configured");
      if (!operatorToken || operatorToken !== expectedToken) throw authError(403, "operator_token_invalid");
      const users = await loadUsers();
      const admin = users.find((item) => item.role === "admin" && item.status !== "disabled");
      if (!admin) throw authError(403, "operator_admin_user_required");
      const sessionId = randomToken();
      const csrfToken = randomToken(24);
      sessions.set(sessionId, {
        userId: admin.id,
        csrfToken,
        createdAt: new Date().toISOString(),
        expiresAt: Date.now() + sessionTtlMs
      });
      setSessionCookie(response, request, sessionId, Math.floor(sessionTtlMs / 1000));
      return { user: publicUser(admin), csrfToken };
    },

    async logout(request, response) {
      const sessionId = parseCookies(request.headers.cookie || "")[sessionCookieName];
      if (sessionId) sessions.delete(sessionId);
      setSessionCookie(response, request, "", 0);
      return { ok: true };
    },

    async requireSession(request, { requireCsrf = false } = {}) {
      const current = await sessionFromRequest(request);
      const csrfHeader = request.headers["x-opl-csrf"] || request.headers["x-opl-csrf-token"];
      if (requireCsrf && csrfHeader !== current.session.csrfToken) {
        throw authError(403, "csrf_token_invalid");
      }
      return {
        user: publicUser(current.user),
        csrfToken: current.session.csrfToken,
        sessionId: current.sessionId
      };
    },

    isAdmin(user) {
      return user?.role === "admin";
    },

    requireAdmin(user) {
      if (user?.role !== "admin") throw authError(403, "admin_role_required");
    },

    accountIdFor(user, requestedAccountId = "") {
      if (user?.role === "admin") return requestedAccountId || user.accountId;
      return user.accountId;
    },

    workspaceInputFor(user, input) {
      if (user?.role === "admin") {
        const scoped = {
          ...input,
          accountId: input.accountId || user.accountId,
          userId: input.userId || user.id
        };
        if (input.organizationId) scoped.organizationId = input.organizationId;
        return scoped;
      }
      return {
        ...input,
        accountId: user.accountId,
        organizationId: user.organizationId || input.organizationId,
        userId: user.id
      };
    },

    async listUsers() {
      return (await loadUsers()).map(publicUser);
    }
  };
}
