import assert from "node:assert/strict";
import test from "node:test";

import { chromium } from "playwright";

import {
  CONSOLE_DEMO_CREDENTIALS,
  startConsoleDemoServer
} from "../../tools/start-console-demo.ts";

test("Console hides secrets and rejects late account data while logout is unconfirmed", async () => {
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    await page.goto(`${demo.origin}/login`, { waitUntil: "networkidle" });
    await page.getByLabel("邮箱").fill(CONSOLE_DEMO_CREDENTIALS.customer.email);
    await page.getByLabel("密码").fill(CONSOLE_DEMO_CREDENTIALS.customer.password);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.waitForURL(/\/console\/overview$/);
    await page.goto(`${demo.origin}/console/workspaces/ws-1`, { waitUntil: "networkidle" });

    const passwordRow = page.locator(".workspace-access-panel .data-list > div").filter({ hasText: "密码" }).first();
    await passwordRow.getByRole("button", { name: "显示", exact: true }).click();
    const password = String(await passwordRow.locator("code").textContent());
    assert.ok(password && !password.includes("••"));
    const workspaceKeyRow = page.locator(".workspace-access-panel .data-list > div").filter({ hasText: "Workspace Key" }).first();
    await workspaceKeyRow.getByRole("button", { name: "显示", exact: true }).click();
    const workspaceKey = String(await workspaceKeyRow.locator("code").textContent());
    assert.ok(workspaceKey && !workspaceKey.includes("••"));

    let releaseSupport: (() => void) | undefined;
    const supportReleased = new Promise<void>((resolve) => { releaseSupport = resolve; });
    let holdSupport: ((route: import("playwright").Route) => void) | undefined;
    const supportHeld = new Promise<import("playwright").Route>((resolve) => { holdSupport = resolve; });
    await page.route("**/api/support/tickets", async (route) => {
      holdSupport?.(route);
      await supportReleased;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ tickets: [{ id: "late-ticket", title: "LATE-ACCOUNT-RESULT" }] })
      });
    });
    await page.getByRole("button", { name: "Support", exact: true }).click();
    await supportHeld;

    let releaseLogout: (() => void) | undefined;
    const logoutReleased = new Promise<void>((resolve) => { releaseLogout = resolve; });
    let holdLogout: ((route: import("playwright").Route) => void) | undefined;
    const logoutHeld = new Promise<import("playwright").Route>((resolve) => { holdLogout = resolve; });
    await page.route("**/api/auth/logout", async (route) => {
      holdLogout?.(route);
      await logoutReleased;
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({ error: "state_persist_failed" })
      });
    });
    await page.getByRole("button", { name: "退出登录", exact: true }).click();
    await logoutHeld;
    await page.getByRole("heading", { name: "正在安全退出", exact: true }).waitFor({ state: "visible" });
    assert.equal(await page.getByText(password, { exact: true }).count(), 0);
    assert.equal(await page.getByText(workspaceKey, { exact: true }).count(), 0);
    assert.equal(await page.getByText(CONSOLE_DEMO_CREDENTIALS.customer.email, { exact: true }).count(), 0);
    assert.equal(await page.getByText("Pilot Workspace", { exact: true }).count(), 0);

    releaseLogout?.();
    await page.getByRole("heading", { name: "退出未确认", exact: true }).waitFor({ state: "visible" });

    assert.match(page.url(), /\/console\/workspaces\/ws-1$/);
    assert.equal(await page.getByText(password, { exact: true }).count(), 0);
    assert.equal(await page.getByText(workspaceKey, { exact: true }).count(), 0);
    assert.equal(await page.getByText(CONSOLE_DEMO_CREDENTIALS.customer.email, { exact: true }).count(), 0);
    assert.equal(await page.getByText("Pilot Workspace", { exact: true }).count(), 0);

    releaseSupport?.();
    await page.getByRole("button", { name: "重试退出", exact: true }).waitFor({ state: "visible" });
    assert.equal(await page.getByText("LATE-ACCOUNT-RESULT", { exact: true }).count(), 0);

    await page.unroute("**/api/auth/logout");
    await page.getByRole("button", { name: "重试退出", exact: true }).click();
    await page.waitForURL(new RegExp(`${demo.origin.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/?$`));
    assert.equal(await page.getByRole("heading", { name: "退出未确认", exact: true }).count(), 0);
  } finally {
    await browser.close();
    await demo.close();
  }
});

test("a late login response cannot restore an account or cancel logout", async () => {
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    await page.addInitScript(() => {
      const originalFetch = window.fetch.bind(window);
      let holdFirstLogin = true;
      let releaseFirstLogin: (() => void) | undefined;
      Object.assign(window, {
        releaseFirstLogin: () => releaseFirstLogin?.()
      });
      window.fetch = (input, init) => {
        const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
        if (holdFirstLogin && new URL(url, window.location.origin).pathname === "/api/auth/login") {
          holdFirstLogin = false;
          return new Promise<Response>((resolve) => {
            releaseFirstLogin = () => resolve(new Response(JSON.stringify({
              user: {
                id: "user-late", accountId: "acct-late", email: "late@example.com", role: "owner", status: "active"
              },
              isOperator: false,
              csrfToken: "csrf-late"
            }), { status: 200, headers: { "content-type": "application/json" } }));
          });
        }
        return originalFetch(input, init);
      };
    });

    await page.goto(`${demo.origin}/login`, { waitUntil: "networkidle" });
    await page.getByLabel("邮箱").fill(CONSOLE_DEMO_CREDENTIALS.customer.email);
    await page.getByLabel("密码").fill(CONSOLE_DEMO_CREDENTIALS.customer.password);
    await page.getByRole("button", { name: "登录", exact: true }).click();

    await page.getByRole("button", { name: "返回", exact: true }).click();
    await page.getByRole("button", { name: "Console 登录", exact: true }).click();
    await page.getByLabel("邮箱").fill(CONSOLE_DEMO_CREDENTIALS.admin.email);
    await page.getByLabel("密码").fill(CONSOLE_DEMO_CREDENTIALS.admin.password);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.waitForURL(/\/console\/overview$/);
    await page.getByRole("link", { name: "API 服务", exact: true }).click();
    await page.getByText("余额历史", { exact: true }).waitFor({ state: "visible" });
    await page.getByRole("button", { name: "退出登录", exact: true }).click();
    await page.waitForURL(`${demo.origin}/`);

    await page.evaluate(() => (window as Window & { releaseFirstLogin?: () => void }).releaseFirstLogin?.());
    await page.waitForTimeout(50);
    assert.equal(page.url(), `${demo.origin}/`);
    assert.equal(await page.getByText("late@example.com", { exact: true }).count(), 0);
    assert.equal(await page.getByText(CONSOLE_DEMO_CREDENTIALS.admin.email, { exact: true }).count(), 0);
    await page.getByRole("button", { name: "Console 登录", exact: true }).waitFor({ state: "visible" });
  } finally {
    await browser.close();
    await demo.close();
  }
});
