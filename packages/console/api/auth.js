import { randomBytes, scrypt as scryptCallback, timingSafeEqual } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const scrypt = promisify(scryptCallback);
const root = fileURLToPath(new URL("../../..", import.meta.url));
const defaultUsersPath = join(root, ".runtime", "opl-console-users.json");
const sessionCookieName = "opl_console_session";
const defaultSessionTtlMs = 24 * 60 * 60 * 1000;

function authError(status, message) {
  const error = new Error(message);
  error.status = status;
  return error;
}

function normalizeEmail(email) {
  return String(email || "").trim().toLowerCase();
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

function ensureAuthUserAccount(state, user) {
  state.accounts ??= {};
  state.users ??= {};
  const account = state.accounts[user.accountId] ?? {
    id: user.accountId,
    balance: 0,
    frozen: 0,
    holds: {}
  };
  const storedUser = state.users[user.id] || user;
  storedUser.accountId ||= user.accountId;
  storedUser.balance = Number(storedUser.balance ?? account.balance ?? 0);
  storedUser.frozen = Number(storedUser.frozen ?? account.frozen ?? 0);
  storedUser.holds ??= account.holds || {};
  storedUser.totalRecharged = Number(storedUser.totalRecharged ?? account.totalRecharged ?? 0);
  state.users[storedUser.id] = storedUser;
  state.accounts[user.accountId] = {
    ...account,
    id: user.accountId,
    balance: storedUser.balance,
    frozen: storedUser.frozen,
    holds: storedUser.holds
  };
  if (account.totalRecharged !== undefined || storedUser.totalRecharged > 0) {
    state.accounts[user.accountId].totalRecharged = storedUser.totalRecharged;
  }
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

async function hashPassword(password, salt = randomBytes(16).toString("hex")) {
  const derived = await scrypt(String(password), salt, 64);
  return `scrypt:${salt}:${derived.toString("hex")}`;
}

async function verifyPassword(password, storedHash) {
  const [scheme, salt, hash] = String(storedHash || "").split(":");
  if (scheme !== "scrypt" || !salt || !hash) return false;
  const derived = await scrypt(String(password), salt, 64);
  const expected = Buffer.from(hash, "hex");
  return expected.length === derived.length && timingSafeEqual(expected, derived);
}

function defaultSeedUsers(env) {
  if (env.OPL_CONSOLE_USERS_JSON) return JSON.parse(env.OPL_CONSOLE_USERS_JSON);
  return [
    {
      id: env.OPL_PI_USER_ID || "usr-pi-alpha",
      email: env.OPL_PI_EMAIL || "pi@opl.local",
      password: env.OPL_PI_PASSWORD || "opl-pi-demo",
      name: env.OPL_PI_NAME || "Pilot PI",
      role: "pi",
      accountId: env.OPL_PI_ACCOUNT_ID || "pi-alpha"
    },
    {
      id: env.OPL_ADMIN_USER_ID || "usr-admin",
      email: env.OPL_ADMIN_EMAIL || "admin@opl.local",
      password: env.OPL_ADMIN_PASSWORD || "opl-admin-demo",
      name: env.OPL_ADMIN_NAME || "OPL Admin",
      role: "admin",
      accountId: env.OPL_ADMIN_ACCOUNT_ID || "admin"
    }
  ];
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

async function readLegacyUsers(usersPath) {
  try {
    return await readUsers(usersPath);
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
    return null;
  }
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
    if (existing.length > 0) {
      return store.update((nextState) => {
        const current = authUsersFromState(nextState);
        for (const user of current) ensureAuthUserAccount(nextState, user);
        return authUsersFromState(nextState);
      });
    }

    const legacyUsers = await readLegacyUsers(usersPath);
    const usersToSeed = legacyUsers?.length
      ? legacyUsers
      : await Promise.all((seedUsers || defaultSeedUsers(env)).map(normalizeSeedUser));

    return store.update((nextState) => {
      const current = authUsersFromState(nextState);
      if (current.length > 0) return current;

      nextState.users ??= {};
      for (const user of usersToSeed) {
        const timestamp = now();
        nextState.users[user.id] = {
          ...nextState.users[user.id],
          ...user,
          createdAt: nextState.users[user.id]?.createdAt || timestamp,
          updatedAt: timestamp
        };
        ensureAuthUserAccount(nextState, user);
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

    async logout(request, response) {
      const sessionId = parseCookies(request.headers.cookie || "")[sessionCookieName];
      if (sessionId) sessions.delete(sessionId);
      setSessionCookie(response, request, "", 0);
      return { ok: true };
    },

    async requireSession(request, { requireCsrf = false } = {}) {
      const current = await sessionFromRequest(request);
      if (requireCsrf && request.headers["x-opl-csrf"] !== current.session.csrfToken) {
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
      if (user?.role === "admin") return input;
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
