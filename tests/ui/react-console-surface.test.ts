import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8").catch(() => "");

async function filesUnder(directory: string): Promise<string[]> {
  const entries = await readdir(new URL(`${directory}/`, root), { withFileTypes: true }).catch(() => []);
  const files: string[] = [];
  for (const entry of entries) {
    const path = `${directory}/${entry.name}`;
    if (entry.isDirectory()) files.push(...await filesUnder(path));
    if (entry.isFile()) files.push(path);
  }
  return files;
}

test("Console runtime is React and Vue is retired", async () => {
  const [packageSource, viteSource, entrySource, contractSource, consoleFiles] = await Promise.all([
    source("package.json"),
    source("vite.config.ts"),
    source("apps/console-ui/src/main.tsx"),
    source("packages/contracts/opl-cloud-console-ui-contract.json"),
    filesUnder("apps/console-ui/src")
  ]);
  const packageJson = JSON.parse(packageSource);
  const contract = JSON.parse(contractSource);

  assert.ok(packageJson.dependencies.react);
  assert.ok(packageJson.dependencies["react-dom"]);
  assert.ok(packageJson.dependencies["@openai/apps-sdk-ui"]);
  assert.ok(packageJson.dependencies["lucide-react"]);
  assert.equal(packageJson.dependencies.vue, undefined);
  assert.equal(packageJson.dependencies["@lucide/vue"], undefined);
  assert.equal(packageJson.dependencies["@vitejs/plugin-vue"], undefined);
  assert.equal(packageJson.devDependencies?.["vue-tsc"], undefined);
  assert.match(viteSource, /@vitejs\/plugin-react/);
  assert.doesNotMatch(viteSource, /plugin-vue|\bvue\(\)/);
  assert.match(entrySource, /createRoot/);
  assert.match(entrySource, /<App\s*\/>/);
  assert.equal(consoleFiles.some((path) => path.endsWith(".vue")), false);
  assert.equal(contract.framework, "react");
  assert.equal(contract.componentFoundation, "@openai/apps-sdk-ui");
});

test("React Console exposes the frozen customer and Admin surfaces", async () => {
  const [model, shell, customerPages, adminPages] = await Promise.all([
    source("apps/console-ui/src/console-model.ts"),
    source("apps/console-ui/src/layout/ConsoleShell.tsx"),
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/pages/AdminPages.tsx")
  ]);
  const joined = [model, shell, customerPages, adminPages].join("\n");
  for (const label of [
    "概览", "Workspace", "API 服务", "账单", "公告",
    "运维概览", "客户与计费账户", "计费复核", "资源状态", "系统状态"
  ]) assert.match(joined, new RegExp(label));
  for (const route of [
    "/console/overview", "/console/workspaces", "/console/api", "/console/billing", "/console/announcements",
    "/admin/overview", "/admin/accounts", "/admin/billing", "/admin/resources", "/admin/system"
  ]) assert.ok(joined.includes(route), route);
});

test("React controller preserves source truth, idempotency and secret lifetime", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  assert.match(controller, /secretLifetimeMs\s*=\s*60_000/);
  assert.match(controller, /clearSecrets/);
  assert.match(controller, /workspaceLaunchIdempotencyKey/);
  assert.match(controller, /requestGeneration/);
  assert.match(controller, /unavailableSource/);
  assert.doesNotMatch(controller, /localStorage|sessionStorage/);
});
