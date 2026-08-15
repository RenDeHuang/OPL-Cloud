import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, rename, unlink, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { dirname, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const qualificationUserID = 41;
const qualificationHost = "0.0.0.0";
const maxBodyBytes = 64 * 1024;

function requiredEnv(env, name) {
  const value = String(env[name] || "").trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function validateSecret(value, name) {
  if (value.length < 32 || value.length > 512) throw new Error(`${name} must contain 32 to 512 characters`);
  return value;
}

function validateQualificationEmail(value) {
  const email = value.toLowerCase();
  if (!/^[^@\s]+@(?:[^@\s]+\.test|localhost|[^@\s]+\.localhost)$/.test(email)) {
    throw new Error("qualification email must use a reserved .test or localhost domain");
  }
  return email;
}

function validateMicros(value, name, { positive = false, negative = false } = {}) {
  const text = String(value);
  if (!/^-?(?:0|[1-9][0-9]*)$/.test(text)) throw new Error(`${name} must be an integer USD-micros string`);
  const micros = BigInt(text);
  if (positive && micros <= 0n || negative && micros >= 0n) throw new Error(`${name} has the wrong sign`);
  if (micros > BigInt(Number.MAX_SAFE_INTEGER) || micros < BigInt(Number.MIN_SAFE_INTEGER)) {
    throw new Error(`${name} exceeds the exact JSON number range`);
  }
  return text;
}

export function qualificationAuthorityConfigFromEnv(env = process.env) {
  const rawPort = String(env.OPL_QUALIFICATION_PORT || "8080");
  if (!/^[1-9][0-9]*$/.test(rawPort) || Number(rawPort) > 65535) {
    throw new Error("OPL_QUALIFICATION_PORT must be between 1 and 65535");
  }
  const email = validateQualificationEmail(requiredEnv(env, "OPL_QUALIFICATION_USER_EMAIL"));
  const password = validateSecret(requiredEnv(env, "OPL_QUALIFICATION_USER_PASSWORD"), "OPL_QUALIFICATION_USER_PASSWORD");
  const userToken = validateSecret(requiredEnv(env, "OPL_QUALIFICATION_USER_TOKEN"), "OPL_QUALIFICATION_USER_TOKEN");
  const authorityToken = validateSecret(requiredEnv(env, "OPL_QUALIFICATION_AUTHORITY_TOKEN"), "OPL_QUALIFICATION_AUTHORITY_TOKEN");
  if (password === userToken || password === authorityToken || userToken === authorityToken) {
    throw new Error("qualification password and tokens must be distinct");
  }
  const statePath = resolve(requiredEnv(env, "OPL_QUALIFICATION_STATE_PATH"));
  const initialUsdMicros = validateMicros(
    requiredEnv(env, "OPL_QUALIFICATION_INITIAL_USD_MICROS"),
    "OPL_QUALIFICATION_INITIAL_USD_MICROS",
    { positive: true }
  );
  return {
    host: qualificationHost,
    port: Number(rawPort),
    userId: qualificationUserID,
    email,
    password,
    userToken,
    authorityToken,
    initialUsdMicros,
    statePath
  };
}

function validateConfig(input) {
  if (!input || input.host !== qualificationHost || input.userId !== qualificationUserID) {
    throw new Error("qualification authority must bind 0.0.0.0 for user 41");
  }
  if (!Number.isInteger(input.port) || input.port < 0 || input.port > 65535) {
    throw new Error("qualification authority port is invalid");
  }
  const config = {
    ...input,
    email: validateQualificationEmail(String(input.email || "")),
    password: validateSecret(String(input.password || ""), "qualification password"),
    userToken: validateSecret(String(input.userToken || ""), "qualification user token"),
    authorityToken: validateSecret(String(input.authorityToken || ""), "qualification authority token"),
    initialUsdMicros: validateMicros(input.initialUsdMicros, "qualification initial USD micros", { positive: true }),
    statePath: resolve(String(input.statePath || ""))
  };
  if (!String(input.statePath || "").trim()) throw new Error("qualification state path is required");
  if (config.password === config.userToken || config.password === config.authorityToken || config.userToken === config.authorityToken) {
    throw new Error("qualification password and tokens must be distinct");
  }
  return config;
}

function refreshToken(config) {
  return `qualification-refresh-${createHash("sha256").update(config.userToken).digest("hex").slice(0, 32)}`;
}

function now() {
  return new Date().toISOString();
}

function initialState(config) {
  const createdAt = now();
  return {
    schemaVersion: 1,
    user: { id: qualificationUserID, email: config.email, status: "active", createdAt, updatedAt: createdAt },
    wallet: { currency: "USD", initialUsdMicros: config.initialUsdMicros, usdMicros: config.initialUsdMicros },
    keys: [],
    keyRequests: {},
    adjustments: [],
    writeCounts: { keyCreates: 0, keyDeletes: 0, debits: 0, refunds: 0 },
    nextKeyId: 700
  };
}

function validStoredState(state, config) {
  return state?.schemaVersion === 1 && state?.user?.id === qualificationUserID && state.user.email === config.email &&
    state.user.status === "active" && state?.wallet?.currency === "USD" &&
    state.wallet.initialUsdMicros === config.initialUsdMicros && /^\d+$/.test(String(state.wallet.usdMicros || "")) &&
    Array.isArray(state.keys) && state.keyRequests && typeof state.keyRequests === "object" && Array.isArray(state.adjustments) &&
    state.adjustments.length <= 2 && state.writeCounts && Number.isInteger(state.nextKeyId);
}

async function loadState(config) {
  try {
    const state = JSON.parse(await readFile(config.statePath, "utf8"));
    if (!validStoredState(state, config)) throw new Error("qualification authority state is incompatible with configured identity");
    return state;
  } catch (error) {
    if (error?.code !== "ENOENT") throw error;
    const state = initialState(config);
    await persistState(config, state);
    return state;
  }
}

async function persistState(config, state) {
  const directory = dirname(config.statePath);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  const temporary = `${config.statePath}.${process.pid}.${randomUUID()}.tmp`;
  try {
    await writeFile(temporary, `${JSON.stringify(state, null, 2)}\n`, { encoding: "utf8", mode: 0o600, flag: "wx" });
    await rename(temporary, config.statePath);
  } catch (error) {
    await unlink(temporary).catch(() => {});
    throw error;
  }
}

function sendJSON(response, status, body) {
  const encoded = JSON.stringify(body);
  response.writeHead(status, {
    "cache-control": "no-store",
    "content-type": "application/json; charset=utf-8",
    "content-length": Buffer.byteLength(encoded)
  });
  response.end(encoded);
}

function success(response, data) {
  sendJSON(response, 200, { code: 0, data });
}

function failure(response, status, code) {
  sendJSON(response, status, { code, data: null });
}

function bearer(request, expected) {
  return request.headers.authorization === `Bearer ${expected}`;
}

async function readJSON(request) {
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > maxBodyBytes) throw new Error("request_too_large");
    chunks.push(chunk);
  }
  if (chunks.length === 0) return {};
  const value = JSON.parse(Buffer.concat(chunks).toString("utf8"));
  if (!value || Array.isArray(value) || typeof value !== "object") throw new Error("invalid_json");
  return value;
}

function usdMicrosFromDecimal(value) {
  if (typeof value !== "number" && typeof value !== "string") throw new Error("invalid_usd_decimal");
  const text = String(value);
  const match = text.match(/^(-?)(0|[1-9][0-9]*)(?:\.([0-9]{1,6}))?$/);
  if (!match) throw new Error("invalid_usd_decimal");
  const micros = BigInt(match[2]) * 1_000_000n + BigInt((match[3] || "").padEnd(6, "0"));
  return (match[1] ? -micros : micros).toString();
}

function usdNumber(microsText) {
  const micros = BigInt(microsText);
  const sign = micros < 0n ? "-" : "";
  const magnitude = micros < 0n ? -micros : micros;
  const decimal = `${sign}${magnitude / 1_000_000n}.${String(magnitude % 1_000_000n).padStart(6, "0")}`;
  return Number(decimal);
}

function requestHash(value) {
  const normalized = {};
  for (const key of Object.keys(value).sort()) normalized[key] = value[key];
  return createHash("sha256").update(JSON.stringify(normalized)).digest("hex");
}

function keyPayload(key) {
  return {
    id: key.id,
    user_id: qualificationUserID,
    name: key.name,
    key: key.value,
    group_id: key.groupId,
    status: key.status,
    ip_whitelist: key.ipWhitelist,
    ip_blacklist: key.ipBlacklist,
    quota: usdNumber(key.quotaUsdMicros),
    quota_used: 0,
    rate_limit_5h: usdNumber(key.rateLimit5hUsdMicros),
    rate_limit_1d: usdNumber(key.rateLimit1dUsdMicros),
    rate_limit_7d: usdNumber(key.rateLimit7dUsdMicros),
    usage_5h: 0,
    usage_1d: 0,
    usage_7d: 0,
    last_used_at: null,
    last_used_ip: null,
    expires_at: key.expiresAt,
    created_at: key.createdAt,
    updated_at: key.updatedAt,
    current_concurrency: 0
  };
}

function pagination(url, defaultSize = 100) {
  const page = Number(url.searchParams.get("page") || "1");
  const pageSize = Number(url.searchParams.get("page_size") || String(defaultSize));
  if (!Number.isInteger(page) || page < 1 || !Number.isInteger(pageSize) || pageSize < 1 || pageSize > 100) {
    throw new Error("invalid_pagination");
  }
  return { page, pageSize };
}

function page(items, pageNumber, pageSize) {
  const pages = Math.max(1, Math.ceil(items.length / pageSize));
  const start = (pageNumber - 1) * pageSize;
  return { items: items.slice(start, start + pageSize), total: items.length, page: pageNumber, page_size: pageSize, pages };
}

function publicState(state) {
  return {
    user: { id: state.user.id, email: state.user.email, status: state.user.status },
    wallet: { ...state.wallet },
    keys: state.keys.map((key) => ({ id: key.id, userId: qualificationUserID, name: key.name, groupId: key.groupId, status: key.status })),
    adjustments: state.adjustments.map((adjustment) => ({
      code: adjustment.code, userId: adjustment.userId, valueUsdMicros: adjustment.valueUsdMicros,
      amountUsdMicros: String(BigInt(adjustment.valueUsdMicros) < 0n ? -BigInt(adjustment.valueUsdMicros) : BigInt(adjustment.valueUsdMicros)),
      kind: BigInt(adjustment.valueUsdMicros) < 0n ? "debit" : "refund", status: adjustment.status, usedAt: adjustment.usedAt
    })),
    writeCounts: { ...state.writeCounts }
  };
}

function userPayload(state) {
  return {
    id: qualificationUserID,
    email: state.user.email,
    balance: usdNumber(state.wallet.usdMicros),
    status: "active",
    created_at: state.user.createdAt,
    updated_at: state.user.updatedAt
  };
}

export async function startQualificationAuthority(input) {
  const config = validateConfig(input);
  let state = await loadState(config);
  let mutationQueue = Promise.resolve();
  const mutate = (callback) => {
    const run = mutationQueue.then(async () => {
      const candidate = structuredClone(state);
      const result = callback(candidate);
      await persistState(config, candidate);
      state = candidate;
      return result;
    });
    mutationQueue = run.catch(() => {});
    return run;
  };

  const server = createServer(async (request, response) => {
    try {
      const url = new URL(request.url || "/", "http://qualification.invalid");
      const method = request.method || "GET";

      if (method === "GET" && url.pathname === "/healthz") {
        success(response, { status: "ready" });
        return;
      }

      if (method === "POST" && url.pathname === "/api/v1/auth/login") {
        const input = await readJSON(request);
        if (String(input.email || "").toLowerCase() !== config.email || input.password !== config.password) {
          failure(response, 401, "invalid_credentials");
          return;
        }
        success(response, {
          access_token: config.userToken,
          refresh_token: refreshToken(config),
          user: { id: qualificationUserID, email: config.email, status: "active" }
        });
        return;
      }

      if (method === "POST" && url.pathname === "/api/v1/auth/refresh") {
        const input = await readJSON(request);
        if (input.refresh_token !== refreshToken(config)) {
          failure(response, 401, "invalid_refresh_token");
          return;
        }
        success(response, { access_token: config.userToken, refresh_token: refreshToken(config) });
        return;
      }

      if (method === "GET" && url.pathname === "/qualification/state") {
        if (!bearer(request, config.authorityToken)) {
          failure(response, 401, "unauthorized");
          return;
        }
        success(response, publicState(state));
        return;
      }

      if (!bearer(request, config.userToken)) {
        failure(response, 401, "unauthorized");
        return;
      }

      if (method === "GET" && url.pathname === "/api/v1/admin/system/version") {
        success(response, { version: "qualification-1" });
        return;
      }

      if (method === "GET" && url.pathname === `/api/v1/admin/users/${qualificationUserID}`) {
        success(response, userPayload(state));
        return;
      }

      if (method === "GET" && url.pathname === "/api/v1/admin/users") {
        const { page: pageNumber, pageSize } = pagination(url);
        const search = String(url.searchParams.get("search") || "").toLowerCase();
        const users = !search || config.email.includes(search) ? [userPayload(state)] : [];
        success(response, page(users, pageNumber, pageSize));
        return;
      }

      if (method === "GET" && url.pathname === "/api/v1/groups/available") {
        success(response, [{
          id: 7,
          name: "Codex",
          description: "Qualification-only delegated group",
          platform: "openai",
          rate_multiplier: 1,
          subscription_type: "standard",
          status: "active"
        }]);
        return;
      }

      if (method === "GET" && url.pathname === "/api/v1/keys") {
        const { page: pageNumber, pageSize } = pagination(url);
        const search = String(url.searchParams.get("search") || "");
        const status = String(url.searchParams.get("status") || "");
        const groupID = String(url.searchParams.get("group_id") || "");
        const keys = state.keys
          .filter((key) => !search || key.name.includes(search))
          .filter((key) => !status || key.status === status)
          .filter((key) => !groupID || String(key.groupId) === groupID)
          .sort((left, right) => left.id - right.id)
          .map(keyPayload);
        success(response, page(keys, pageNumber, pageSize));
        return;
      }

      if (method === "POST" && url.pathname === "/api/v1/keys") {
        const idempotencyKey = String(request.headers["idempotency-key"] || "").trim();
        const input = await readJSON(request);
        const name = String(input.name || "").trim();
        const groupId = Number(input.group_id);
        if (!idempotencyKey || !name || groupId !== 7) {
          failure(response, 400, "invalid_key");
          return;
        }
        let quotaUsdMicros;
        let rateLimit5hUsdMicros;
        let rateLimit1dUsdMicros;
        let rateLimit7dUsdMicros;
        try {
          quotaUsdMicros = usdMicrosFromDecimal(input.quota ?? 0);
          rateLimit5hUsdMicros = usdMicrosFromDecimal(input.rate_limit_5h ?? 0);
          rateLimit1dUsdMicros = usdMicrosFromDecimal(input.rate_limit_1d ?? 0);
          rateLimit7dUsdMicros = usdMicrosFromDecimal(input.rate_limit_7d ?? 0);
        } catch {
          failure(response, 400, "invalid_key");
          return;
        }
        if ([quotaUsdMicros, rateLimit5hUsdMicros, rateLimit1dUsdMicros, rateLimit7dUsdMicros].some((value) => BigInt(value) < 0n)) {
          failure(response, 400, "invalid_key");
          return;
        }
        const normalized = {
          name,
          groupId,
          quotaUsdMicros,
          rateLimit5hUsdMicros,
          rateLimit1dUsdMicros,
          rateLimit7dUsdMicros,
          ipWhitelist: Array.isArray(input.ip_whitelist) ? input.ip_whitelist : [],
          ipBlacklist: Array.isArray(input.ip_blacklist) ? input.ip_blacklist : [],
          expiresInDays: input.expires_in_days ?? null
        };
        const hash = requestHash(normalized);
        const keyResult = await mutate((current) => {
          const prior = current.keyRequests[idempotencyKey];
          if (prior) {
            const replayed = current.keys.find((candidate) => candidate.id === prior.keyId);
            return prior.requestHash === hash && replayed
              ? { status: "replayed", key: replayed }
              : { status: "conflict" };
          }
          const createdAt = now();
          current.nextKeyId += 1;
          const created = {
            id: current.nextKeyId,
            name,
            value: `sk-qualification-${createHash("sha256").update(idempotencyKey).digest("hex").slice(0, 24)}`,
            groupId,
            status: "active",
            quotaUsdMicros,
            rateLimit5hUsdMicros,
            rateLimit1dUsdMicros,
            rateLimit7dUsdMicros,
            ipWhitelist: normalized.ipWhitelist,
            ipBlacklist: normalized.ipBlacklist,
            expiresAt: normalized.expiresInDays === null ? null : new Date(Date.now() + Number(normalized.expiresInDays) * 86_400_000).toISOString(),
            createdAt,
            updatedAt: createdAt
          };
          current.keys.push(created);
          current.keyRequests[idempotencyKey] = { requestHash: hash, keyId: created.id };
          current.writeCounts.keyCreates += 1;
          return { status: "created", key: created };
        });
        if (keyResult.status === "conflict") {
          failure(response, 409, "key_idempotency_conflict");
          return;
        }
        success(response, keyPayload(keyResult.key));
        return;
      }

      const delegatedKeyMatch = url.pathname.match(/^\/api\/v1\/keys\/([1-9][0-9]*)$/);
      if (delegatedKeyMatch && method === "GET") {
        const key = state.keys.find((candidate) => candidate.id === Number(delegatedKeyMatch[1]));
        if (!key) {
          failure(response, 404, "key_not_found");
          return;
        }
        success(response, keyPayload(key));
        return;
      }
      if (delegatedKeyMatch && method === "DELETE") {
        const keyID = Number(delegatedKeyMatch[1]);
        const key = state.keys.find((candidate) => candidate.id === keyID);
        if (key) {
          await mutate((current) => {
            current.keys = current.keys.filter((candidate) => candidate.id !== keyID);
            current.writeCounts.keyDeletes += 1;
          });
        }
        success(response, { deleted: true, id: keyID });
        return;
      }

      if (method === "GET" && url.pathname === "/api/v1/admin/usage/search-api-keys") {
        if (url.searchParams.get("user_id") !== String(qualificationUserID)) {
          failure(response, 404, "user_not_found");
          return;
        }
        const query = String(url.searchParams.get("q") || "");
        const keys = state.keys
          .filter((key) => !query || key.name === query)
          .map((key) => ({ id: key.id, name: key.name, user_id: qualificationUserID }));
        success(response, keys);
        return;
      }

      if (method === "GET" && url.pathname === `/api/v1/admin/users/${qualificationUserID}/api-keys`) {
        const { page: pageNumber, pageSize } = pagination(url);
        const keys = state.keys.slice().sort((left, right) => left.id - right.id).map(keyPayload);
        success(response, page(keys, pageNumber, pageSize));
        return;
      }

      if (method === "GET" && url.pathname === "/api/v1/admin/usage") {
        const { page: pageNumber, pageSize } = pagination(url, 50);
        success(response, page([], pageNumber, pageSize));
        return;
      }

      if (method === "GET" && url.pathname === "/api/v1/admin/usage/stats") {
        success(response, { total_requests: 0, total_input_tokens: 0, total_output_tokens: 0, total_tokens: 0, total_actual_cost: 0 });
        return;
      }

      if (method === "POST" && url.pathname === "/api/v1/admin/redeem-codes/create-and-redeem") {
        const idempotencyKey = String(request.headers["idempotency-key"] || "").trim();
        const input = await readJSON(request);
        let valueUsdMicros;
        try {
          valueUsdMicros = usdMicrosFromDecimal(input.value);
        } catch {
          failure(response, 400, "invalid_redeem_value");
          return;
        }
        const normalized = {
          code: String(input.code || ""),
          type: input.type,
          valueUsdMicros,
          userId: input.user_id,
          notes: String(input.notes || "")
        };
        if (!normalized.code || normalized.code !== idempotencyKey || normalized.type !== "balance" || normalized.userId !== qualificationUserID || BigInt(valueUsdMicros) === 0n) {
          failure(response, 400, "invalid_redeem");
          return;
        }
        const hash = requestHash(normalized);
        const adjustmentResult = await mutate((current) => {
          const prior = current.adjustments.find((candidate) => candidate.code === normalized.code);
          if (prior) {
            return prior.requestHash === hash ? { status: "replayed", adjustment: prior } : { status: "payload_conflict" };
          }
          const kind = BigInt(valueUsdMicros) < 0n ? "debit" : "refund";
          if (current.adjustments.some((candidate) => (BigInt(candidate.valueUsdMicros) < 0n ? "debit" : "refund") === kind)) {
          return { status: "identity_conflict", code: kind === "debit" ? "debit_identity_conflict" : "refund_identity_conflict" };
          }
          if (BigInt(current.wallet.usdMicros) + BigInt(valueUsdMicros) < 0n) {
            return { status: "insufficient_balance" };
          }
          const usedAt = now();
          const created = {
            code: normalized.code,
            userId: qualificationUserID,
            valueUsdMicros,
            notes: normalized.notes,
            status: "used",
            usedAt,
            createdAt: usedAt,
            requestHash: hash
          };
          current.wallet.usdMicros = String(BigInt(current.wallet.usdMicros) + BigInt(valueUsdMicros));
          current.user.updatedAt = usedAt;
          current.adjustments.push(created);
          current.writeCounts[kind === "debit" ? "debits" : "refunds"] += 1;
          return { status: "created", adjustment: created };
        });
        if (adjustmentResult.status === "payload_conflict" || adjustmentResult.status === "identity_conflict") {
          failure(response, 409, adjustmentResult.status === "payload_conflict" ? "redeem_conflict" : (adjustmentResult.code || "adjustment_identity_conflict"));
          return;
        }
        if (adjustmentResult.status === "insufficient_balance") {
          failure(response, 409, "insufficient_balance");
          return;
        }
        const debit = adjustmentResult.adjustment;
        success(response, { redeem_code: {
          code: debit.code,
          type: "balance",
          value: usdNumber(debit.valueUsdMicros),
          status: debit.status,
          used_by: debit.userId
        } });
        return;
      }

      if (method === "GET" && url.pathname === `/api/v1/admin/users/${qualificationUserID}/balance-history`) {
        const { page: pageNumber, pageSize } = pagination(url);
        const history = state.adjustments.slice().reverse().map((debit) => ({
          code: debit.code,
          type: "balance",
          value: usdNumber(debit.valueUsdMicros),
          status: debit.status,
          used_by: debit.userId,
          used_at: debit.usedAt,
          created_at: debit.createdAt
        }));
        success(response, page(history, pageNumber, pageSize));
        return;
      }

      failure(response, 404, "not_found");
    } catch (error) {
      const code = error instanceof SyntaxError ? "invalid_json" : String(error?.message || "request_failed");
      failure(response, code === "request_too_large" ? 413 : 400, code);
    }
  });

  await new Promise((resolvePromise, reject) => {
    server.once("error", reject);
    server.listen(config.port, config.host, () => {
      server.removeListener("error", reject);
      resolvePromise();
    });
  });
  const address = server.address();
  if (!address || typeof address === "string") {
    server.close();
    throw new Error("qualification authority address is unavailable");
  }
  let closed = false;
  return {
    origin: `http://127.0.0.1:${address.port}`,
    async close() {
      if (closed) return;
      closed = true;
      await new Promise((resolvePromise, reject) => server.close((error) => error ? reject(error) : resolvePromise()));
    }
  };
}

async function main() {
  const authority = await startQualificationAuthority(qualificationAuthorityConfigFromEnv());
  process.stdout.write(`${JSON.stringify({ status: "READY", origin: authority.origin })}\n`);
  const stop = () => void authority.close().finally(() => process.exit(0));
  process.once("SIGINT", stop);
  process.once("SIGTERM", stop);
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
