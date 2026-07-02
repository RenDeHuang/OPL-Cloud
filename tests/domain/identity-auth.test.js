import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import test from "node:test";

import { createRequestHandler } from "../../services/api/server.js";
import { createOplCloud } from "../../services/api/src/opl-cloud.js";
import { MemoryStore } from "../../services/api/src/store.js";

const pricing = {
  serverHourly: { basic: 1, pro: 4 },
  diskGbMonth: 0.2,
  markup: 0.2
};

async function listen(handler) {
  const server = createServer(handler);
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  return {
    origin: `http://127.0.0.1:${address.port}`,
    close: () => new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
  };
}

function createService() {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: { name: "test-provider" },
    pricing
  });
}

async function postJson(origin, path, body, { cookie = "", csrf = "" } = {}) {
  const headers = { "content-type": "application/json" };
  if (cookie) headers.cookie = cookie;
  if (csrf) headers["x-opl-csrf-token"] = csrf;
  const response = await fetch(`${origin}${path}`, {
    method: "POST",
    headers,
    body: JSON.stringify(body)
  });
  const payload = await response.json();
  return {
    response,
    payload,
    cookie: response.headers.get("set-cookie") || cookie,
    csrf: response.headers.get("x-opl-csrf-token") || payload.csrfToken || csrf
  };
}

test("operator invites a PI, PI accepts the invite, then logs in with email and password", async () => {
  const appService = createService();
  const { origin, close } = await listen(createRequestHandler({
    appService,
    enforceSessionScope: true,
    operatorSummaryToken: "operator-token",
    enforceCsrf: true
  }));

  try {
    const operatorLogin = await postJson(origin, "/api/auth/operator-login", {
      accountId: "operator",
      tenantId: "ops",
      displayName: "OPL Operator",
      email: "ops@example.com",
      operatorToken: "operator-token"
    });
    assert.equal(operatorLogin.response.status, 200);
    assert.match(operatorLogin.cookie, /opl_console_session=/);
    assert.ok(operatorLogin.csrf);

    const invite = await postJson(origin, "/api/operator/users/invite", {
      email: "pi-invite@example.com",
      accountId: "pi-invite",
      tenantId: "tenant-invite",
      displayName: "Invited PI",
      role: "pi"
    }, {
      cookie: operatorLogin.cookie,
      csrf: operatorLogin.csrf
    });
    assert.equal(invite.response.status, 200);
    assert.match(invite.payload.inviteToken, /^invite_/);
    assert.equal(invite.payload.user.email, "pi-invite@example.com");
    assert.equal(invite.payload.user.status, "invited");

    const accepted = await postJson(origin, "/api/auth/accept-invite", {
      inviteToken: invite.payload.inviteToken,
      password: "correct horse battery staple"
    });
    assert.equal(accepted.response.status, 200);
    assert.equal(accepted.payload.accountId, "pi-invite");
    assert.equal(accepted.payload.email, "pi-invite@example.com");
    assert.equal(accepted.payload.role, "pi");
    assert.ok(accepted.csrf);

    const login = await postJson(origin, "/api/auth/login", {
      email: "pi-invite@example.com",
      password: "correct horse battery staple"
    });
    assert.equal(login.response.status, 200);
    assert.match(login.cookie, /HttpOnly/);
    assert.equal(login.payload.accountId, "pi-invite");

    const session = await (await fetch(`${origin}/api/session`, { headers: { cookie: login.cookie } })).json();
    assert.equal(session.accountId, "pi-invite");
    assert.equal(session.email, "pi-invite@example.com");

    const state = await (await fetch(`${origin}/api/state`, { headers: { cookie: login.cookie } })).json();
    assert.equal(state.account.id, "pi-invite");
    assert.ok(state.audit.some((event) => event.type === "identity.user_invited"));
    assert.ok(state.audit.some((event) => event.type === "identity.invite_accepted"));
    assert.ok(state.audit.some((event) => event.type === "identity.user_login"));
  } finally {
    await close();
  }
});

test("operator creates a commercial PI account with an initial password and no invite step", async () => {
  const appService = createService();
  const { origin, close } = await listen(createRequestHandler({
    appService,
    enforceSessionScope: true,
    operatorSummaryToken: "operator-token",
    enforceCsrf: true
  }));

  try {
    const operatorLogin = await postJson(origin, "/api/auth/operator-login", {
      accountId: "operator",
      tenantId: "ops",
      displayName: "OPL Operator",
      email: "ops@example.com",
      operatorToken: "operator-token"
    });

    const created = await postJson(origin, "/api/operator/users", {
      email: "pi-commercial@example.com",
      accountId: "pi-commercial",
      tenantId: "tenant-commercial",
      displayName: "Commercial PI",
      password: "correct horse battery staple"
    }, {
      cookie: operatorLogin.cookie,
      csrf: operatorLogin.csrf
    });
    assert.equal(created.response.status, 200);
    assert.equal(created.payload.user.email, "pi-commercial@example.com");
    assert.equal(created.payload.user.role, "pi");
    assert.equal(created.payload.user.status, "active");
    assert.equal(created.payload.inviteToken, undefined);

    const login = await postJson(origin, "/api/auth/login", {
      email: "pi-commercial@example.com",
      password: "correct horse battery staple"
    });
    assert.equal(login.response.status, 200);
    assert.equal(login.payload.accountId, "pi-commercial");

    const state = await (await fetch(`${origin}/api/state`, { headers: { cookie: login.cookie } })).json();
    assert.equal(state.account.id, "pi-commercial");
    assert.ok(state.audit.some((event) => event.type === "identity.user_created"));
    assert.equal(state.audit.some((event) => event.type === "identity.invite_accepted"), false);
  } finally {
    await close();
  }
});

test("operator can disable a user and both old sessions and new logins fail closed", async () => {
  const appService = createService();
  const { origin, close } = await listen(createRequestHandler({
    appService,
    enforceSessionScope: true,
    operatorSummaryToken: "operator-token",
    enforceCsrf: true
  }));

  try {
    const operatorLogin = await postJson(origin, "/api/auth/operator-login", {
      accountId: "operator",
      email: "ops@example.com",
      displayName: "OPL Operator",
      operatorToken: "operator-token"
    });
    const invite = await postJson(origin, "/api/operator/users/invite", {
      email: "disabled-pi@example.com",
      accountId: "pi-disabled",
      tenantId: "tenant-disabled",
      displayName: "Disabled PI",
      role: "pi"
    }, {
      cookie: operatorLogin.cookie,
      csrf: operatorLogin.csrf
    });
    await postJson(origin, "/api/auth/accept-invite", {
      inviteToken: invite.payload.inviteToken,
      password: "correct horse battery staple"
    });
    const piLogin = await postJson(origin, "/api/auth/login", {
      email: "disabled-pi@example.com",
      password: "correct horse battery staple"
    });
    assert.equal(piLogin.response.status, 200);

    const disabled = await postJson(origin, "/api/operator/users/disable", {
      email: "disabled-pi@example.com",
      reason: "pilot access revoked"
    }, {
      cookie: operatorLogin.cookie,
      csrf: operatorLogin.csrf
    });
    assert.equal(disabled.response.status, 200);
    assert.equal(disabled.payload.user.status, "disabled");

    const oldSessionResponse = await fetch(`${origin}/api/session`, {
      headers: { cookie: piLogin.cookie }
    });
    const oldSession = await oldSessionResponse.json();
    assert.equal(oldSessionResponse.status, 401);
    assert.equal(oldSession.error, "user_disabled");

    const loginAgain = await postJson(origin, "/api/auth/login", {
      email: "disabled-pi@example.com",
      password: "correct horse battery staple"
    });
    assert.equal(loginAgain.response.status, 403);
    assert.equal(loginAgain.payload.error, "user_disabled");
  } finally {
    await close();
  }
});

test("authenticated write APIs require CSRF and production mode can mark cookies Secure", async () => {
  const appService = createService();
  const { origin, close } = await listen(createRequestHandler({
    appService,
    enforceSessionScope: true,
    operatorSummaryToken: "operator-token",
    enforceCsrf: true,
    cookieSecure: true
  }));

  try {
    const operatorLogin = await postJson(origin, "/api/auth/operator-login", {
      accountId: "operator",
      email: "ops@example.com",
      displayName: "OPL Operator",
      operatorToken: "operator-token"
    });
    assert.match(operatorLogin.cookie, /Secure/);
    assert.ok(operatorLogin.csrf);

    const blocked = await postJson(origin, "/api/accounts/credit", {
      accountId: "pi-csrf",
      amount: 100,
      reason: "manual_top_up"
    }, {
      cookie: operatorLogin.cookie
    });
    assert.equal(blocked.response.status, 403);
    assert.equal(blocked.payload.error, "csrf_required");

    const credited = await postJson(origin, "/api/accounts/credit", {
      accountId: "pi-csrf",
      amount: 100,
      reason: "manual_top_up"
    }, {
      cookie: operatorLogin.cookie,
      csrf: operatorLogin.csrf
    });
    assert.equal(credited.response.status, 200);
    assert.equal(credited.payload.balance, 100);
  } finally {
    await close();
  }
});

test("logout clears the server-side session and expires the console cookie", async () => {
  const appService = createService();
  const { origin, close } = await listen(createRequestHandler({
    appService,
    enforceSessionScope: true,
    operatorSummaryToken: "operator-token",
    enforceCsrf: true
  }));

  try {
    const operatorLogin = await postJson(origin, "/api/auth/operator-login", {
      accountId: "operator",
      email: "ops@example.com",
      displayName: "OPL Operator",
      operatorToken: "operator-token"
    });
    assert.equal(operatorLogin.response.status, 200);
    assert.match(operatorLogin.cookie, /opl_console_session=/);
    assert.ok(operatorLogin.csrf);

    const logout = await postJson(origin, "/api/auth/logout", {}, {
      cookie: operatorLogin.cookie,
      csrf: operatorLogin.csrf
    });
    assert.equal(logout.response.status, 200);
    assert.equal(logout.payload.ok, true);
    assert.match(logout.cookie, /Max-Age=0/);

    const oldSessionResponse = await fetch(`${origin}/api/session`, {
      headers: { cookie: operatorLogin.cookie }
    });
    const oldSession = await oldSessionResponse.json();
    assert.equal(oldSessionResponse.status, 401);
    assert.equal(oldSession.error, "authentication_required");
  } finally {
    await close();
  }
});

test("commercial API defaults require a real session and stay logged out after logout", async () => {
  const appService = createService();
  const { origin, close } = await listen(createRequestHandler({
    appService,
    operatorSummaryToken: "operator-token"
  }));

  try {
    const anonymousSessionResponse = await fetch(`${origin}/api/session`);
    const anonymousSession = await anonymousSessionResponse.json();
    assert.equal(anonymousSessionResponse.status, 401);
    assert.equal(anonymousSession.error, "authentication_required");

    const operatorLogin = await postJson(origin, "/api/auth/operator-login", {
      accountId: "operator",
      email: "ops@example.com",
      displayName: "OPL Operator",
      operatorToken: "operator-token"
    });
    assert.equal(operatorLogin.response.status, 200);
    assert.match(operatorLogin.cookie, /opl_console_session=/);
    assert.ok(operatorLogin.csrf);

    const logout = await postJson(origin, "/api/auth/logout", {}, {
      cookie: operatorLogin.cookie,
      csrf: operatorLogin.csrf
    });
    assert.equal(logout.response.status, 200);
    assert.match(logout.cookie, /Max-Age=0/);

    const oldCookieResponse = await fetch(`${origin}/api/session`, {
      headers: { cookie: operatorLogin.cookie }
    });
    const oldCookieSession = await oldCookieResponse.json();
    assert.equal(oldCookieResponse.status, 401);
    assert.equal(oldCookieSession.error, "authentication_required");

    const noCookieResponse = await fetch(`${origin}/api/session`);
    const noCookieSession = await noCookieResponse.json();
    assert.equal(noCookieResponse.status, 401);
    assert.equal(noCookieSession.error, "authentication_required");
  } finally {
    await close();
  }
});
