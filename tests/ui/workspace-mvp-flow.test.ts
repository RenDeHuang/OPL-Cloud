import assert from "node:assert/strict";
import { afterEach, test } from "node:test";

import { chromium } from "playwright";

import * as workspaceApi from "../../apps/console-ui/src/api/workspaces-api.ts";
import {
  CONSOLE_DEMO_CREDENTIALS,
  startConsoleDemoServer
} from "../../tools/start-console-demo.ts";

const originalFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = originalFetch;
});

test("Workspace delete adapter sends one typed Control Plane command", async () => {
  const requests: Array<{ path: string; init?: RequestInit }> = [];
  globalThis.fetch = async (input, init) => {
    requests.push({ path: String(input), init });
    return new Response(JSON.stringify({
      workspaceId: "workspace / alpha",
      status: "deleted",
      operationId: "delete-alpha"
    }), { status: 200, headers: { "content-type": "application/json" } });
  };

  const result = await workspaceApi.deleteWorkspace("workspace / alpha", "csrf-alpha", "delete-once");

  assert.deepEqual(result, {
    available: true,
    data: { workspaceId: "workspace / alpha", status: "deleted", operationId: "delete-alpha" }
  });
  assert.equal(requests.length, 1);
  assert.equal(requests[0].path, "/api/workspaces/workspace%20%2F%20alpha");
  assert.equal(requests[0].init?.method, "DELETE");
  assert.equal(new Headers(requests[0].init?.headers).get("x-opl-csrf"), "csrf-alpha");
  assert.equal(new Headers(requests[0].init?.headers).get("Idempotency-Key"), "delete-once");
});

test("Workspace delete adapter reports an unlanded backend route as unavailable", async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({ error: "method_not_allowed" }), {
    status: 405,
    headers: { "content-type": "application/json" }
  });

  assert.deepEqual(await workspaceApi.deleteWorkspace("workspace-alpha", "csrf-alpha", "delete-once"), {
    available: false,
    reasonCode: "workspace_delete_unavailable"
  });
});

test("Workspace delete does not relabel an owner not-found response as route unavailability", async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({ error: "workspace_not_found" }), {
    status: 404,
    headers: { "content-type": "application/json" }
  });

  await assert.rejects(
    () => workspaceApi.deleteWorkspace("workspace-alpha", "csrf-alpha", "delete-once"),
    /workspace_not_found/
  );
});

test("Workspace delete releases command busy after a route change without applying the late result", async () => {
  const demo = await startConsoleDemoServer({ port: 0, log: false });
  const browser = await chromium.launch({ headless: true });
  let releaseDelete: (() => void) | undefined;
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    let workspaceListReads = 0;
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (request.method() === "GET" && url.pathname === "/api/workspaces") workspaceListReads += 1;
    });

    await page.goto(`${demo.origin}/login`, { waitUntil: "networkidle" });
    await page.getByLabel("邮箱").fill(CONSOLE_DEMO_CREDENTIALS.customer.email);
    await page.getByLabel("密码").fill(CONSOLE_DEMO_CREDENTIALS.customer.password);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.waitForURL(/\/console\/overview$/);
    await page.goto(`${demo.origin}/console/workspaces/ws-1`, { waitUntil: "networkidle" });

    let holdDelete: ((route: import("playwright").Route) => void) | undefined;
    const deleteHeld = new Promise<import("playwright").Route>((resolve) => { holdDelete = resolve; });
    const deleteReleased = new Promise<void>((resolve) => { releaseDelete = resolve; });
    await page.route("**/api/workspaces/ws-1", async (route) => {
      if (route.request().method() !== "DELETE") {
        await route.continue();
        return;
      }
      holdDelete?.(route);
      await deleteReleased;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ workspaceId: "ws-1", status: "deleted", operationId: "delete-ws-1" })
      });
    });

    page.once("dialog", (dialog) => { void dialog.accept(); });
    const firstDelete = page.getByRole("button", { name: "删除 Workspace", exact: true });
    await firstDelete.click();
    await deleteHeld;
    assert.equal(await firstDelete.getAttribute("aria-busy"), "true");

    await page.getByRole("button", { name: "Workspace 列表", exact: true }).click();
    await page.waitForURL(/\/console\/workspaces$/);
    await page.locator(".workspace-list-row").filter({ hasText: "Second Workspace" }).click();
    await page.waitForURL(/\/console\/workspaces\/ws-2$/);
    await page.getByRole("heading", { name: "Second Workspace", exact: true }).waitFor({ state: "visible" });

    const secondDelete = page.getByRole("button", { name: "删除 Workspace", exact: true });
    assert.equal(await secondDelete.getAttribute("aria-busy"), "true");
    assert.equal(await secondDelete.isDisabled(), true);
    const readsBeforeRelease = workspaceListReads;

    releaseDelete?.();
    await page.waitForFunction(() => {
      const button = [...document.querySelectorAll("button")]
        .find((candidate) => candidate.textContent?.includes("删除 Workspace"));
      return Boolean(button && !button.hasAttribute("disabled") && button.getAttribute("aria-busy") !== "true");
    });

    assert.equal(await secondDelete.isDisabled(), false);
    assert.equal(workspaceListReads, readsBeforeRelease);
    assert.match(page.url(), /\/console\/workspaces\/ws-2$/);
    assert.equal(await page.getByText("Workspace 已删除", { exact: true }).count(), 0);
    assert.equal(await page.getByText("删除结果尚未获得权威回读确认", { exact: true }).count(), 0);
  } finally {
    releaseDelete?.();
    await browser.close();
    await demo.close();
  }
});
