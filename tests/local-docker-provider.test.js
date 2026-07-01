import assert from "node:assert/strict";
import { mkdtemp, rm, stat, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { createOplCloud } from "../services/api/src/opl-cloud.js";
import { LocalDockerProvider } from "../services/api/src/runtime-providers/local-docker.js";
import { MemoryStore } from "../services/api/src/store.js";

async function exists(path) {
  try {
    await stat(path);
    return true;
  } catch {
    return false;
  }
}

test("local Docker provider creates real compose, disk, URL, and preserves disk after server destroy", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-local-docker-"));
  try {
    const provider = new LocalDockerProvider({
      rootDir: root,
      baseUrl: "http://127.0.0.1:8787",
      execute: false
    });
    const service = createOplCloud({
      store: new MemoryStore(),
      runtimeProvider: provider,
      pricing: {
        serverHourly: { basic: 1, pro: 4 },
        diskGbMonth: 0.2,
        markup: 0.1
      }
    });

    await service.creditAccount({ accountId: "pi-alpha", amount: 200, reason: "owner_credit" });
    const workspace = await service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "Real Docker Lab",
      packageId: "basic"
    });

    const workspaceDir = join(root, workspace.id);
    const composePath = join(workspaceDir, "docker-compose.yml");
    const diskPath = join(workspaceDir, "disk");
    const envPath = join(workspaceDir, ".env");

    assert.equal(workspace.provider, "local-docker");
    assert.equal(workspace.server.status, "running");
    assert.equal(workspace.docker.status, "running");
    assert.equal(workspace.disk.status, "attached_retained");
    assert.equal(workspace.disk.localPath, diskPath);
    assert.equal(workspace.url, `http://127.0.0.1:8787/workspaces/${workspace.slug}?token=${workspace.access.token}`);
    assert.equal(await exists(composePath), true);
    assert.equal(await exists(diskPath), true);
    assert.equal(await exists(envPath), true);

    const compose = await readFile(composePath, "utf8");
    assert.match(compose, /ghcr\.io\/gaofeng21cn\/one-person-lab-webui:latest/);
    assert.match(compose, /\/data/);
    assert.doesNotMatch(compose, /- \.\/workspace:\/data/);
    assert.match(compose, /OPL_WORKSPACE_ID/);
    assert.match(compose, /OPL_WEBUI_AUTH_MODE: none/);
    assert.match(compose, /HOME: \/data/);
    assert.match(compose, /OPL_WORKSPACE_ROOT: \/data\/workspaces/);
    assert.match(compose, /CODEX_HOME: \/data\/codex/);

    const destroyed = await service.destroyServer({
      accountId: "pi-alpha",
      workspaceId: workspace.id,
      confirm: true
    });

    assert.equal(destroyed.server.status, "destroyed");
    assert.equal(destroyed.disk.status, "detached_retained");
    assert.equal(await exists(diskPath), true);

    const diskDestroyed = await service.destroyDisk({
      accountId: "pi-alpha",
      workspaceId: workspace.id,
      confirmDataLoss: true
    });
    assert.equal(diskDestroyed.disk.status, "destroyed");
    assert.equal(await exists(diskPath), false);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
