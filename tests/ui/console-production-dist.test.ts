import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { readFile, stat } from "node:fs/promises";
import { createServer } from "node:http";
import { extname, relative, resolve } from "node:path";
import { promisify } from "node:util";
import test from "node:test";

import { chromium } from "playwright";

const execFileAsync = promisify(execFile);
const root = resolve(import.meta.dirname, "../..");
const dist = resolve(root, "dist");
const reactHomeHeading = "工作区、API 服务与账单，在一个权威控制面里。";
const viewports = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "mobile", width: 390, height: 844 }
] as const;
const contentTypes: Record<string, string> = {
  ".css": "text/css; charset=utf-8",
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".png": "image/png",
  ".svg": "image/svg+xml",
  ".woff2": "font/woff2"
};

async function buildProductionDist() {
  const npm = process.platform === "win32" ? "npm.cmd" : "npm";
  const { stdout, stderr } = await execFileAsync(npm, ["run", "build"], {
    cwd: root,
    maxBuffer: 10 * 1024 * 1024
  });
  return `${stdout}${stderr}`;
}

async function startDistServer() {
  const server = createServer(async (request, response) => {
    try {
      const pathname = decodeURIComponent(new URL(request.url || "/", "http://127.0.0.1").pathname);
      const requestedPath = pathname === "/" ? "index.html" : pathname.replace(/^\/+/, "");
      let filePath = resolve(dist, requestedPath);
      if (relative(dist, filePath).startsWith("..")) {
        response.writeHead(400).end();
        return;
      }
      try {
        if (!(await stat(filePath)).isFile()) throw new Error("not_a_file");
      } catch {
        if (pathname.startsWith("/assets/")) {
          response.writeHead(404).end();
          return;
        }
        filePath = resolve(dist, "index.html");
      }
      response.writeHead(200, { "content-type": contentTypes[extname(filePath)] || "application/octet-stream" });
      response.end(await readFile(filePath));
    } catch {
      response.writeHead(500).end();
    }
  });

  await new Promise<void>((resolveListen, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      resolveListen();
    });
  });
  const address = server.address();
  assert.ok(address && typeof address !== "string", "production dist server must bind a local port");
  return {
    origin: `http://127.0.0.1:${address.port}`,
    close: () => new Promise<void>((resolveClose, reject) => {
      server.close((error) => error ? reject(error) : resolveClose());
    })
  };
}

test("production dist boots the React Console at desktop and mobile", { timeout: 120_000 }, async () => {
  const buildOutput = await buildProductionDist();
  const server = await startDistServer();
  const browser = await chromium.launch({ headless: true });
  const evidence = [];
  try {
    for (const viewport of viewports) {
      const context = await browser.newContext({ viewport });
      const page = await context.newPage();
      const pageErrors: string[] = [];
      const consoleErrors: string[] = [];
      const assetFailures: string[] = [];
      page.on("pageerror", (error) => pageErrors.push(error.message));
      page.on("console", (message) => {
        if (message.type() === "error") consoleErrors.push(message.text());
      });
      page.on("requestfailed", (request) => {
        if (["script", "stylesheet"].includes(request.resourceType())) {
          assetFailures.push(`${request.url()}: ${request.failure()?.errorText || "request_failed"}`);
        }
      });
      page.on("response", (response) => {
        if (["script", "stylesheet"].includes(response.request().resourceType()) && !response.ok()) {
          assetFailures.push(`${response.url()}: HTTP ${response.status()}`);
        }
      });

      await page.goto(server.origin, { waitUntil: "load" });
      const heading = page.getByRole("heading", { name: reactHomeHeading, exact: true });
      const reactPageVisible = await heading.waitFor({ state: "visible", timeout: 5_000 }).then(() => true, () => false);
      const visibleText = (await page.locator("body").innerText()).trim();
      evidence.push({
        viewport: viewport.name,
        reactPageVisible,
        visibleTextLength: visibleText.length,
        pageErrors,
        consoleErrors,
        assetFailures
      });
      await context.close();
    }
  } finally {
    await browser.close();
    await server.close();
  }

  for (const item of evidence) {
    const message = JSON.stringify(evidence, null, 2);
    assert.equal(item.reactPageVisible, true, message);
    assert.ok(item.visibleTextLength > 0, message);
    assert.deepEqual(item.pageErrors, [], message);
    assert.deepEqual(item.consoleErrors, [], message);
    assert.deepEqual(item.assetFailures, [], message);
  }
  assert.doesNotMatch(buildOutput, /Circular chunk:/, buildOutput);
});
