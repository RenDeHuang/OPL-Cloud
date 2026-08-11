import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createServer as createHttpServer } from "node:http";
import { connect } from "node:net";
import test from "node:test";

import { chromium } from "playwright";

import {
  CONSOLE_DEMO_CREDENTIALS,
  startConsoleDemoServer
} from "../../tools/start-console-demo.ts";

class DemoClient {
  cookie = "";
  origin: string;

  constructor(origin: string) {
    this.origin = origin;
  }

  async request(path: string, init: RequestInit = {}) {
    const headers = new Headers(init.headers);
    if (this.cookie) headers.set("cookie", this.cookie);
    const response = await fetch(`${this.origin}${path}`, { ...init, headers });
    const setCookie = response.headers.get("set-cookie");
    if (setCookie) this.cookie = setCookie.split(";", 1)[0];
    return response;
  }

  postJson(path: string, body: unknown, headers: HeadersInit = {}) {
    const requestHeaders = new Headers(headers);
    requestHeaders.set("content-type", "application/json");
    return this.request(path, {
      method: "POST",
      headers: requestHeaders,
      body: JSON.stringify(body)
    });
  }

  postCommandJson(path: string, body: unknown, idempotencyKey: string) {
    return this.postJson(path, body, { "idempotency-key": idempotencyKey });
  }
}

async function listen(server: ReturnType<typeof createHttpServer>) {
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      resolve();
    });
  });
  const address = server.address();
  assert.ok(address && typeof address !== "string");
  return `http://127.0.0.1:${address.port}`;
}

async function closeHttpServer(server: ReturnType<typeof createHttpServer>) {
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
}

test("package exposes an explicit localhost-only Console demo command", async () => {
  const packageJson = JSON.parse(await readFile("package.json", "utf8"));
  assert.equal(packageJson.scripts.demo, "node tools/start-console-demo.ts");
});

test("Console demo isolates customer, Admin, and anonymous sessions", async () => {
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  try {
    assert.match(demo.origin, /^http:\/\/127\.0\.0\.1:\d+$/);
    const customerClient = new DemoClient(demo.origin);
    const adminClient = new DemoClient(demo.origin);
    const anonymousClient = new DemoClient(demo.origin);

    const anonymous = await anonymousClient.request("/api/auth/me");
    assert.equal(anonymous.status, 401);
    assert.equal((await anonymousClient.request("/api/workspaces?page=1&pageSize=10")).status, 401);

    const customerLogin = await customerClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.customer);
    assert.equal(customerLogin.status, 200);
    const customer = await customerLogin.json();
    assert.equal(customer.user.role, "owner");
    assert.equal(customer.isOperator, false);

    const customerSession = await customerClient.request("/api/auth/me");
    assert.equal(customerSession.status, 200);
    assert.equal((await customerSession.json()).data.role, "owner");
    assert.equal((await customerClient.request("/api/operator/overview")).status, 403);
    assert.equal((await anonymousClient.request("/api/auth/me")).status, 401);

    const adminLogin = await adminClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.admin);
    assert.equal(adminLogin.status, 200);
    const admin = await adminLogin.json();
    assert.equal(admin.user.role, "admin");
    assert.equal(admin.isOperator, true);
    assert.equal((await adminClient.request("/api/operator/overview")).status, 200);
    assert.equal((await customerClient.request("/api/auth/me")).status, 200);

    const logout = await customerClient.postJson("/api/auth/logout", {});
    assert.equal(logout.status, 200);
    assert.equal((await customerClient.request("/api/auth/me")).status, 401);
    assert.equal((await adminClient.request("/api/auth/me")).status, 200);
  } finally {
    await demo.close();
  }
});

test("Console demo rotates a caller-supplied Session ID during login", async () => {
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  try {
    const victimClient = new DemoClient(demo.origin);
    const attackerClient = new DemoClient(demo.origin);
    victimClient.cookie = "opl_console_demo_session=attacker-chosen";
    attackerClient.cookie = "opl_console_demo_session=attacker-chosen";

    assert.equal((await attackerClient.request("/api/auth/me")).status, 401);
    assert.equal((await victimClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.admin)).status, 200);
    assert.notEqual(victimClient.cookie, attackerClient.cookie);
    assert.equal((await victimClient.request("/api/auth/me")).status, 200);
    assert.equal((await attackerClient.request("/api/auth/me")).status, 401);
  } finally {
    await demo.close();
  }
});

test("Console demo assigns unique owner-scoped general API Keys", async () => {
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  try {
    const customerClient = new DemoClient(demo.origin);
    const adminClient = new DemoClient(demo.origin);
    assert.equal((await customerClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.customer)).status, 200);
    assert.equal((await adminClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.admin)).status, 200);

    const customerCreate = await customerClient.postJson(
      "/api/gateway/keys",
      { name: "customer-key", groupId: "101" },
      { "idempotency-key": "customer-key-create" }
    );
    const adminCreate = await adminClient.postJson(
      "/api/gateway/keys",
      { name: "admin-key", groupId: "101" },
      { "idempotency-key": "admin-key-create" }
    );
    assert.equal(customerCreate.status, 200);
    assert.equal(adminCreate.status, 200);
    const customerKey = (await customerCreate.json()).data;
    const adminKey = (await adminCreate.json()).data;
    assert.equal(customerKey.name, "customer-key");
    assert.equal(adminKey.name, "admin-key");
    assert.notEqual(customerKey.id, adminKey.id);
    assert.equal(Object.hasOwn(customerKey, "ownerAccountId"), false);
    assert.equal(Object.hasOwn(customerKey, "secret"), false);

    const customerKeys = (await (await customerClient.request("/api/gateway/keys")).json()).data.items;
    const adminKeys = (await (await adminClient.request("/api/gateway/keys")).json()).data.items;
    assert.deepEqual(customerKeys.map((item) => item.name), ["General fixture key", "customer-key"]);
    assert.deepEqual(adminKeys.map((item) => item.name), ["admin-key"]);

    assert.equal((await customerClient.postJson(`/api/gateway/keys/${adminKey.id}/reveal`, {})).status, 404);
    assert.equal((await adminClient.postJson(`/api/gateway/keys/${customerKey.id}/reveal`, {})).status, 404);
    const customerSecret = (await (await customerClient.postJson(`/api/gateway/keys/${customerKey.id}/reveal`, {})).json()).data.value;
    const adminSecret = (await (await adminClient.postJson(`/api/gateway/keys/${adminKey.id}/reveal`, {})).json()).data.value;
    assert.notEqual(customerSecret, adminSecret);
  } finally {
    await demo.close();
  }
});

test("Console demo assigns unique owner-scoped Workspace Keys", async () => {
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  try {
    const customerClient = new DemoClient(demo.origin);
    const adminClient = new DemoClient(demo.origin);
    assert.equal((await customerClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.customer)).status, 200);
    assert.equal((await adminClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.admin)).status, 200);

    const customerLaunch = await customerClient.postCommandJson(
      "/api/workspace-launches",
      { name: "Customer Workspace", packageId: "basic" },
      "customer-workspace-key-launch"
    );
    const adminLaunch = await adminClient.postCommandJson(
      "/api/workspace-launches",
      { name: "Admin Workspace", packageId: "basic" },
      "admin-workspace-key-launch"
    );
    assert.equal(customerLaunch.status, 200);
    assert.equal(adminLaunch.status, 200);
    const customerWorkspaceId = (await customerLaunch.json()).workspaceId;
    const adminWorkspaceId = (await adminLaunch.json()).workspaceId;
    const customerWorkspaces = (await (await customerClient.request("/api/workspaces?page=1&pageSize=20")).json()).data.items;
    const adminWorkspaces = (await (await adminClient.request("/api/workspaces?page=1&pageSize=20")).json()).data.items;
    const customerKeyId = customerWorkspaces.find((item) => item.id === customerWorkspaceId).workspaceApiKeyId;
    const adminKeyId = adminWorkspaces.find((item) => item.id === adminWorkspaceId).workspaceApiKeyId;
    assert.notEqual(customerKeyId, adminKeyId);

    assert.equal((await customerClient.postJson(`/api/gateway/keys/${adminKeyId}/reveal`, {})).status, 404);
    assert.equal((await adminClient.postJson(`/api/gateway/keys/${customerKeyId}/reveal`, {})).status, 404);
    const customerSecret = (await (await customerClient.postJson(`/api/gateway/keys/${customerKeyId}/reveal`, {})).json()).data.value;
    const adminSecret = (await (await adminClient.postJson(`/api/gateway/keys/${adminKeyId}/reveal`, {})).json()).data.value;
    assert.notEqual(customerSecret, adminSecret);
  } finally {
    await demo.close();
  }
});

test("Console demo keeps a unique persistent Runtime credential per Workspace", async () => {
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  try {
    const customerClient = new DemoClient(demo.origin);
    const adminClient = new DemoClient(demo.origin);
    assert.equal((await customerClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.customer)).status, 200);
    assert.equal((await adminClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.admin)).status, 200);

    const customerWorkspaceId = (await (await customerClient.postCommandJson(
      "/api/workspace-launches",
      { name: "Customer Runtime", packageId: "basic" },
      "customer-runtime-launch"
    )).json()).workspaceId;
    const adminWorkspaceId = (await (await adminClient.postCommandJson(
      "/api/workspace-launches",
      { name: "Admin Runtime", packageId: "basic" },
      "admin-runtime-launch"
    )).json()).workspaceId;

    assert.equal((await customerClient.postJson(`/api/workspaces/${adminWorkspaceId}/runtime-credentials/reveal`, {})).status, 404);
    assert.equal((await adminClient.postJson(`/api/workspaces/${customerWorkspaceId}/runtime-credentials/reveal`, {})).status, 404);
    const customerInitial = await (await customerClient.postJson(`/api/workspaces/${customerWorkspaceId}/runtime-credentials/reveal`, {})).json();
    const adminInitial = await (await adminClient.postJson(`/api/workspaces/${adminWorkspaceId}/runtime-credentials/reveal`, {})).json();
    assert.notEqual(customerInitial.access.password, adminInitial.access.password);

    const customerRotated = await (await customerClient.postJson(`/api/workspaces/${customerWorkspaceId}/runtime-credentials/rotate`, {})).json();
    const adminRotated = await (await adminClient.postJson(`/api/workspaces/${adminWorkspaceId}/runtime-credentials/rotate`, {})).json();
    assert.notEqual(customerRotated.access.password, customerInitial.access.password);
    assert.notEqual(adminRotated.access.password, adminInitial.access.password);
    assert.notEqual(customerRotated.access.password, adminRotated.access.password);

    const customerReadback = await (await customerClient.postJson(`/api/workspaces/${customerWorkspaceId}/runtime-credentials/reveal`, {})).json();
    const adminReadback = await (await adminClient.postJson(`/api/workspaces/${adminWorkspaceId}/runtime-credentials/reveal`, {})).json();
    assert.equal(customerReadback.access.password, customerRotated.access.password);
    assert.equal(adminReadback.access.password, adminRotated.access.password);
    assert.equal(customerReadback.access.credentialVersion, "2");
    assert.equal(adminReadback.access.credentialVersion, "2");
  } finally {
    await demo.close();
  }
});

test("Console demo preserves owner-scoped launch records across accounts", async () => {
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  try {
    const customerClient = new DemoClient(demo.origin);
    const adminClient = new DemoClient(demo.origin);
    assert.equal((await customerClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.customer)).status, 200);
    assert.equal((await adminClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.admin)).status, 200);

    const customerLaunch = await (await customerClient.postCommandJson(
      "/api/workspace-launches",
      { name: "Customer Launch", packageId: "basic" },
      "customer-record-launch"
    )).json();
    const adminLaunch = await (await adminClient.postCommandJson(
      "/api/workspace-launches",
      { name: "Admin Launch", packageId: "pro" },
      "admin-record-launch"
    )).json();
    const customerLaunches = await (await customerClient.request("/api/workspace-launches")).json();
    const adminLaunches = await (await adminClient.request("/api/workspace-launches")).json();
    assert.deepEqual(customerLaunches.map((item) => item.operationId), [customerLaunch.operationId]);
    assert.deepEqual(adminLaunches.map((item) => item.operationId), [adminLaunch.operationId]);

    assert.equal((await customerClient.request(`/api/workspace-launches/${customerLaunch.operationId}`)).status, 200);
    assert.equal((await adminClient.request(`/api/workspace-launches/${adminLaunch.operationId}`)).status, 200);
    assert.equal((await customerClient.request(`/api/workspace-launches/${adminLaunch.operationId}`)).status, 404);
    assert.equal((await adminClient.request(`/api/workspace-launches/${customerLaunch.operationId}`)).status, 404);
  } finally {
    await demo.close();
  }
});

test("Console demo forbids Admin access to another account Workspace secret", async () => {
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  try {
    const customerClient = new DemoClient(demo.origin);
    const adminClient = new DemoClient(demo.origin);
    assert.equal((await customerClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.customer)).status, 200);
    assert.equal((await adminClient.postJson("/api/auth/login", CONSOLE_DEMO_CREDENTIALS.admin)).status, 200);

    assert.equal((await customerClient.postJson("/api/workspaces/ws-1/runtime-credentials/reveal", {})).status, 200);
    assert.equal((await adminClient.postJson("/api/workspaces/ws-1/runtime-credentials/reveal", {})).status, 404);
  } finally {
    await demo.close();
  }
});

test("Console demo fails closed for every API-like path without using the configured Vite proxy", async () => {
  let proxyHits = 0;
  const proxy = createHttpServer((_request, response) => {
    proxyHits += 1;
    response.statusCode = 418;
    response.end("external proxy reached");
  });
  const proxyOrigin = await listen(proxy);
  const previousOrigin = process.env.OPL_CONSOLE_API_ORIGIN;
  process.env.OPL_CONSOLE_API_ORIGIN = proxyOrigin;
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  try {
    for (const path of ["/api", "/api?probe=1", "/apiX", "/api%2Foperator%2Faccounts"]) {
      const response = await fetch(`${demo.origin}${path}`);
      assert.equal(response.status, 404, path);
      assert.deepEqual(await response.json(), { error: "console_demo_api_not_found" });
    }
    assert.equal(proxyHits, 0);
  } finally {
    await demo.close();
    await closeHttpServer(proxy);
    if (previousOrigin === undefined) delete process.env.OPL_CONSOLE_API_ORIGIN;
    else process.env.OPL_CONSOLE_API_ORIGIN = previousOrigin;
  }
});

test("Console demo closes within a bounded time when a client leaves a request body incomplete", async () => {
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  const port = Number(new URL(demo.origin).port);
  const socket = connect({ host: "127.0.0.1", port });
  await new Promise<void>((resolve, reject) => {
    socket.once("connect", resolve);
    socket.once("error", reject);
  });
  socket.write([
    "POST /api/auth/login HTTP/1.1",
    `Host: 127.0.0.1:${port}`,
    "Content-Type: application/json",
    "Content-Length: 1000",
    "",
    "{"
  ].join("\r\n"));

  const closing = demo.close();
  try {
    await Promise.race([
      closing,
      new Promise((_, reject) => setTimeout(() => reject(new Error("console_demo_close_timeout")), 750))
    ]);
  } finally {
    socket.destroy();
    await closing;
  }
});

test("Console demo is clickable in a normal browser for customer and Admin", async () => {
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    await page.goto(`${demo.origin}/login`, { waitUntil: "networkidle" });
    await page.getByLabel("邮箱").fill(CONSOLE_DEMO_CREDENTIALS.customer.email);
    await page.getByLabel("密码").fill(CONSOLE_DEMO_CREDENTIALS.customer.password);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.waitForURL(/\/console\/overview$/);
    await page.getByRole("link", { name: "Workspace", exact: true }).click();
    await page.getByText("Pilot Workspace", { exact: true }).waitFor({ state: "visible" });
    await page.getByRole("link", { name: "API 服务", exact: true }).click();
    await page.getByText("余额历史", { exact: true }).waitFor({ state: "visible" });

    await page.getByRole("button", { name: "退出登录", exact: true }).click();
    await page.waitForURL(new RegExp(`${demo.origin.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/?$`));
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.waitForURL(/\/login$/);
    await page.getByLabel("邮箱").fill(CONSOLE_DEMO_CREDENTIALS.admin.email);
    await page.getByLabel("密码").fill(CONSOLE_DEMO_CREDENTIALS.admin.password);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.waitForURL(/\/console\/overview$/);
    await page.getByRole("link", { name: "运维概览", exact: true }).click();
    await page.waitForURL(/\/admin\/overview$/);
    await page.getByRole("heading", { name: "运维概览", exact: true }).waitFor({ state: "visible" });
  } finally {
    await browser.close();
    await demo.close();
  }
});
