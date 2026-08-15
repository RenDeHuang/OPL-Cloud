import assert from "node:assert/strict";
import { mkdtemp, readFile, readdir, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  qualificationAuthorityConfigFromEnv,
  startQualificationAuthority
} from "../../tools/local-sub2api-authority-fixture.ts";

const credentials = Object.freeze({
  email: "qualification-admin@example.test",
  password: "qualification-password-32-characters",
  userToken: "qualification-user-token-32-characters",
  authorityToken: "qualification-authority-token-32-chars"
});

async function jsonRequest(origin, path, { method = "GET", token = "", idempotencyKey = "", body } = {}) {
  const headers = {};
  if (token) headers.authorization = `Bearer ${token}`;
  if (idempotencyKey) headers["idempotency-key"] = idempotencyKey;
  if (body !== undefined) headers["content-type"] = "application/json";
  const response = await fetch(`${origin}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  return { status: response.status, payload: await response.json() };
}

function assertSuccess(result) {
  assert.equal(result.status, 200);
  assert.deepEqual(Object.keys(result.payload).sort(), ["code", "data"]);
  assert.equal(result.payload.code, 0);
  return result.payload.data;
}

test("qualification authority configuration is explicit and rejects unsafe input", () => {
  const statePath = join(tmpdir(), "qualification-authority-config.json");
  const config = qualificationAuthorityConfigFromEnv({
    OPL_QUALIFICATION_PORT: "18080",
    OPL_QUALIFICATION_USER_EMAIL: credentials.email,
    OPL_QUALIFICATION_USER_PASSWORD: credentials.password,
    OPL_QUALIFICATION_USER_TOKEN: credentials.userToken,
    OPL_QUALIFICATION_AUTHORITY_TOKEN: credentials.authorityToken,
    OPL_QUALIFICATION_INITIAL_USD_MICROS: "100000000",
    OPL_QUALIFICATION_STATE_PATH: statePath
  });
  assert.deepEqual(config, {
    host: "0.0.0.0",
    port: 18080,
    userId: 41,
    email: credentials.email,
    password: credentials.password,
    userToken: credentials.userToken,
    authorityToken: credentials.authorityToken,
    initialUsdMicros: "100000000",
    statePath
  });
  assert.throws(() => qualificationAuthorityConfigFromEnv({}), /OPL_QUALIFICATION_USER_EMAIL/);
  assert.throws(() => qualificationAuthorityConfigFromEnv({
    OPL_QUALIFICATION_PORT: "8080",
    OPL_QUALIFICATION_USER_EMAIL: "admin@invalid.invalid",
    OPL_QUALIFICATION_USER_PASSWORD: credentials.password,
    OPL_QUALIFICATION_USER_TOKEN: credentials.userToken,
    OPL_QUALIFICATION_AUTHORITY_TOKEN: credentials.authorityToken,
    OPL_QUALIFICATION_INITIAL_USD_MICROS: "100000000",
    OPL_QUALIFICATION_STATE_PATH: statePath
  }), /qualification email/);
});

test("qualification authority provides one persistent user, key, and exact debit identity", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "opl-local-sub2api-"));
  const statePath = join(directory, "authority.json");
  t.after(async () => rm(directory, { recursive: true, force: true }));

  const config = {
    host: "0.0.0.0",
    port: 0,
    userId: 41,
    ...credentials,
    initialUsdMicros: "100000000",
    statePath
  };
  let authority = await startQualificationAuthority(config);
  t.after(async () => authority.close());

  const health = assertSuccess(await jsonRequest(authority.origin, "/healthz"));
  assert.deepEqual(health, { status: "ready" });

  const badLogin = await jsonRequest(authority.origin, "/api/v1/auth/login", {
    method: "POST",
    body: { email: credentials.email, password: "wrong-password" }
  });
  assert.equal(badLogin.status, 401);
  assert.equal(badLogin.payload.code, "invalid_credentials");

  const login = assertSuccess(await jsonRequest(authority.origin, "/api/v1/auth/login", {
    method: "POST",
    body: { email: credentials.email, password: credentials.password }
  }));
  assert.deepEqual(login.user, { id: 41, email: credentials.email, status: "active" });
  assert.equal(login.access_token, credentials.userToken);
  assert.ok(login.refresh_token);

  const refresh = assertSuccess(await jsonRequest(authority.origin, "/api/v1/auth/refresh", {
    method: "POST",
    body: { refresh_token: login.refresh_token }
  }));
  assert.equal(refresh.access_token, credentials.userToken);
  assert.equal(refresh.refresh_token, login.refresh_token);

  const version = assertSuccess(await jsonRequest(authority.origin, "/api/v1/admin/system/version", {
    token: credentials.userToken
  }));
  assert.match(version.version, /^qualification-/);

  const user = assertSuccess(await jsonRequest(authority.origin, "/api/v1/admin/users/41", {
    token: credentials.userToken
  }));
  assert.equal(user.id, 41);
  assert.equal(user.email, credentials.email);
  assert.equal(user.balance, 100);

  const users = assertSuccess(await jsonRequest(
    authority.origin,
    `/api/v1/admin/users?page=1&page_size=100&search=${encodeURIComponent(credentials.email)}&sort_by=id&sort_order=asc`,
    { token: credentials.userToken }
  ));
  assert.equal(users.total, 1);
  assert.deepEqual(users.items.map((item) => item.id), [41]);
  const otherUser = await jsonRequest(authority.origin, "/api/v1/admin/users/42", {
    token: credentials.userToken
  });
  assert.equal(otherUser.status, 404);
  assert.equal(otherUser.payload.code, "not_found");

  const groups = assertSuccess(await jsonRequest(authority.origin, "/api/v1/groups/available", {
    token: credentials.userToken
  }));
  assert.deepEqual(groups.map((group) => group.id), [7]);

  const keyInput = { name: "opl-workspace", group_id: 7, quota: 0 };
  const key = assertSuccess(await jsonRequest(authority.origin, "/api/v1/keys", {
    method: "POST",
    token: credentials.userToken,
    idempotencyKey: "qualification-key-1",
    body: keyInput
  }));
  assert.equal(key.user_id, 41);
  assert.equal(key.name, "opl-workspace");
  assert.match(key.key, /^sk-qualification-/);

  const keyReplay = assertSuccess(await jsonRequest(authority.origin, "/api/v1/keys", {
    method: "POST",
    token: credentials.userToken,
    idempotencyKey: "qualification-key-1",
    body: keyInput
  }));
  assert.deepEqual(keyReplay, key);
  const keyConflict = await jsonRequest(authority.origin, "/api/v1/keys", {
    method: "POST",
    token: credentials.userToken,
    idempotencyKey: "qualification-key-1",
    body: { ...keyInput, name: "changed-workspace-key" }
  });
  assert.equal(keyConflict.status, 409);
  assert.equal(keyConflict.payload.code, "key_idempotency_conflict");

  const delegatedList = assertSuccess(await jsonRequest(
    authority.origin,
    "/api/v1/keys?page=1&page_size=100&search=opl-workspace&sort_by=id&sort_order=asc",
    { token: credentials.userToken }
  ));
  assert.equal(delegatedList.total, 1);
  assert.deepEqual(delegatedList.items, [key]);

  const delegatedGet = assertSuccess(await jsonRequest(authority.origin, `/api/v1/keys/${key.id}`, {
    token: credentials.userToken
  }));
  assert.deepEqual(delegatedGet, key);

  const adminSearch = assertSuccess(await jsonRequest(
    authority.origin,
    "/api/v1/admin/usage/search-api-keys?user_id=41&q=opl-workspace",
    { token: credentials.userToken }
  ));
  assert.deepEqual(adminSearch, [{ id: key.id, name: key.name, user_id: 41 }]);

  const adminKeys = assertSuccess(await jsonRequest(
    authority.origin,
    "/api/v1/admin/users/41/api-keys?page=1&page_size=1&sort_by=id&sort_order=asc",
    { token: credentials.userToken }
  ));
  assert.equal(adminKeys.total, 1);
  assert.deepEqual(adminKeys.items, [key]);

  const debitInput = { code: "opl:qualification:debit:1", type: "balance", value: -52.58, user_id: 41, notes: "local qualification" };
  const debit = assertSuccess(await jsonRequest(authority.origin, "/api/v1/admin/redeem-codes/create-and-redeem", {
    method: "POST",
    token: credentials.userToken,
    idempotencyKey: debitInput.code,
    body: debitInput
  }));
  assert.deepEqual(debit.redeem_code, {
    code: debitInput.code,
    type: "balance",
    value: -52.58,
    status: "used",
    used_by: 41
  });

  const debitReplay = assertSuccess(await jsonRequest(authority.origin, "/api/v1/admin/redeem-codes/create-and-redeem", {
    method: "POST",
    token: credentials.userToken,
    idempotencyKey: debitInput.code,
    body: debitInput
  }));
  assert.deepEqual(debitReplay, debit);

  const conflict = await jsonRequest(authority.origin, "/api/v1/admin/redeem-codes/create-and-redeem", {
    method: "POST",
    token: credentials.userToken,
    idempotencyKey: debitInput.code,
    body: { ...debitInput, value: -1 }
  });
  assert.equal(conflict.status, 409);
  assert.equal(conflict.payload.code, "redeem_conflict");

  const secondIdentity = await jsonRequest(authority.origin, "/api/v1/admin/redeem-codes/create-and-redeem", {
    method: "POST",
    token: credentials.userToken,
    idempotencyKey: "opl:qualification:debit:2",
    body: { ...debitInput, code: "opl:qualification:debit:2" }
  });
  assert.equal(secondIdentity.status, 409);
  assert.equal(secondIdentity.payload.code, "debit_identity_conflict");

  const history = assertSuccess(await jsonRequest(
    authority.origin,
    "/api/v1/admin/users/41/balance-history?page=1&page_size=100&type=balance",
    { token: credentials.userToken }
  ));
  assert.equal(history.total, 1);
  assert.equal(history.items[0].code, debitInput.code);
  assert.equal(history.items[0].value, -52.58);

  const refundInput = { code: "opl:qualification:refund:1", type: "balance", value: 52.58, user_id: 41, notes: "local qualification cleanup" };
  const refund = assertSuccess(await jsonRequest(authority.origin, "/api/v1/admin/redeem-codes/create-and-redeem", {
    method: "POST", token: credentials.userToken, idempotencyKey: refundInput.code, body: refundInput
  }));
  assert.equal(refund.redeem_code.value, 52.58);
  const refundReplay = assertSuccess(await jsonRequest(authority.origin, "/api/v1/admin/redeem-codes/create-and-redeem", {
    method: "POST", token: credentials.userToken, idempotencyKey: refundInput.code, body: refundInput
  }));
  assert.deepEqual(refundReplay, refund);

  const unauthorizedState = await jsonRequest(authority.origin, "/qualification/state", {
    token: credentials.userToken
  });
  assert.equal(unauthorizedState.status, 401);

  const state = assertSuccess(await jsonRequest(authority.origin, "/qualification/state", {
    token: credentials.authorityToken
  }));
  assert.deepEqual(state.user, { id: 41, email: credentials.email, status: "active" });
  assert.deepEqual(state.wallet, { currency: "USD", initialUsdMicros: "100000000", usdMicros: "100000000" });
  assert.equal(state.keys.length, 1);
  assert.equal(state.adjustments.length, 2);
  assert.deepEqual(state.writeCounts, { keyCreates: 1, keyDeletes: 0, debits: 1, refunds: 1 });

  const usage = await jsonRequest(authority.origin, "/api/v1/admin/usage?page=1&page_size=20", {
    token: credentials.userToken
  });
  assert.equal(usage.status, 200);
  assert.equal(usage.payload.code, 0);
  assert.equal(usage.payload.data.total, 0);
  const unknown = await jsonRequest(authority.origin, "/api/v1/unknown", { token: credentials.userToken });
  assert.equal(unknown.status, 404);
  assert.equal(unknown.payload.code, "not_found");

  await authority.close();
  authority = await startQualificationAuthority(config);
  const persisted = assertSuccess(await jsonRequest(authority.origin, "/qualification/state", {
    token: credentials.authorityToken
  }));
  assert.deepEqual(persisted, state);
  const disk = JSON.parse(await readFile(statePath, "utf8"));
  assert.equal(disk.wallet.usdMicros, "100000000");
  assert.deepEqual(await readdir(directory), ["authority.json"]);
  assert.equal((await stat(statePath)).mode & 0o777, 0o600);

  const deleted = assertSuccess(await jsonRequest(authority.origin, `/api/v1/keys/${key.id}`, {
    method: "DELETE",
    token: credentials.userToken
  }));
  assert.deepEqual(deleted, { deleted: true, id: key.id });
  const afterDelete = assertSuccess(await jsonRequest(authority.origin, "/qualification/state", {
    token: credentials.authorityToken
  }));
  assert.equal(afterDelete.keys.length, 0);
  assert.deepEqual(afterDelete.writeCounts, { keyCreates: 1, keyDeletes: 1, debits: 1, refunds: 1 });
});

test("fixture source contains no public domain or credential literals", async () => {
  const source = [
    await readFile(new URL("../../tools/local-sub2api-authority-fixture.ts", import.meta.url), "utf8"),
    await readFile(new URL("local-sub2api-authority-fixture.test.ts", import.meta.url), "utf8")
  ].join("\n");
  assert.doesNotMatch(source, /(?:https?:\/\/|@)[^\s"'`]+\.(?:com|cn|net|org)\b/i);
  assert.doesNotMatch(source, new RegExp(["gh", "p_"].join(""), "i"));
  assert.doesNotMatch(source, new RegExp(["github", "_pat_"].join(""), "i"));
});
