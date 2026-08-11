import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { afterEach, test } from "node:test";

import * as workspaceApi from "../../apps/console-ui/src/api/workspaces-api.ts";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8");
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

test("Workspace create and delete success both require authoritative list readback", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  const createReadback = controller.slice(
    controller.indexOf("const confirmWorkspaceLaunchReadback"),
    controller.indexOf("const pollWorkspaceLaunch")
  );
  const deleteFlow = controller.slice(
    controller.indexOf("const confirmWorkspaceDeleteReadback"),
    controller.indexOf("const revealWorkspacePassword")
  );

  assert.match(createReadback, /const detail = await findWorkspaceInPages\(workspaceId\)/);
  assert.match(createReadback, /if \(!detail\.available \|\| detail\.data === null\)/);
  assert.ok(createReadback.indexOf("findWorkspaceInPages") < createReadback.indexOf("Workspace 已开通"));
  assert.match(deleteFlow, /deleteWorkspaceCommand/);
  assert.match(deleteFlow, /const readback = await findWorkspaceInPages\(workspaceId\)/);
  assert.match(deleteFlow, /if \(!readback\.available \|\| readback\.data !== null\)/);
  assert.ok(deleteFlow.indexOf("findWorkspaceInPages") < deleteFlow.indexOf("Workspace 已删除"));
  assert.match(deleteFlow, /workspaceDeleteIntent\.current\.idempotencyKey/);
  assert.match(deleteFlow, /workspaceIdFromPath\(window\.location\.pathname\) === workspace\.id/);
  assert.match(deleteFlow, /apiErrorCode\(error\) === "workspace_not_found"[\s\S]+confirmWorkspaceDeleteReadback/);
  assert.match(controller, /clearSecrets\(\);\s+setWorkspaceDeleteIssue\(""\);\s+setSidebarOpen/);
});

test("Workspace detail exposes authoritative WebUI access and honest delete states", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const detail = pages.slice(
    pages.indexOf("function WorkspaceDetailPage"),
    pages.indexOf("function ApiTabs")
  );

  assert.match(detail, /runtimeData\.url && window\.open\(runtimeData\.url/);
  assert.match(detail, /打开 WebUI/);
  assert.match(detail, /deleteCurrentWorkspace/);
  assert.match(detail, /删除 Workspace/);
  assert.match(detail, /workspace_delete_unavailable/);
  assert.match(detail, /删除结果待确认/);
  assert.doesNotMatch(detail, /provider|Fabric|Tencent|Kubernetes|localStorage|sessionStorage/i);
});
