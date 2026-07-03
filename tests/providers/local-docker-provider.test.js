import assert from "node:assert/strict";
import { mkdtemp, rm, stat, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { parse } from "yaml";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { LocalDockerProvider } from "../../packages/fabric/src/runtime-providers/local-docker.js";
import { MemoryStore } from "../../packages/console/src/store.js";

async function exists(path) {
  try {
    await stat(path);
    return true;
  } catch {
    return false;
  }
}

test("local Docker provider exposes split compute, storage, attachment, and Workspace entry operations", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-local-resource-"));
  try {
    const provider = new LocalDockerProvider({
      rootDir: root,
      baseUrl: "http://127.0.0.1:8787",
      execute: false
    });
    const packagePlan = { id: "basic", server: "2c4g", diskGb: 10 };

    const storage = await provider.createStorageVolume({
      storageId: "storage-local001",
      storage: { id: "storage-local001", name: "Grant data" },
      packagePlan
    });
    const compute = await provider.createComputeResource({
      computeId: "compute-local001",
      compute: { id: "compute-local001", name: "CPU node" },
      packagePlan
    });
    const attachment = await provider.attachStorage({
      attachment: {
        id: "attach-local001",
        computeId: "compute-local001",
        storageId: "storage-local001",
        mountPath: "/data"
      },
      compute: { id: "compute-local001", name: "CPU node", ...compute },
      storage: { id: "storage-local001", name: "Grant data", ...storage }
    });
    const entry = await provider.createWorkspaceEntry({
      workspaceId: "ws-local001",
      workspaceName: "Grant Lab",
      slug: "grant-lab-local001",
      token: "share_local_secret",
      attachment: { id: "attach-local001", mountPath: "/data", ...attachment },
      compute: { id: "compute-local001", name: "CPU node", ...compute },
      storage: { id: "storage-local001", name: "Grant data", ...storage },
      packagePlan
    });

    const compose = parse(await readFile(compute.composePath, "utf8"));
    const composeService = Object.values(compose.services)[0];

    assert.equal(storage.status, "available");
    assert.equal(await exists(join(storage.localPath, "data")), true);
    assert.equal(await exists(join(storage.localPath, "projects")), true);
    assert.equal(compute.status, "running");
    assert.equal(attachment.status, "attached");
    assert.equal(entry.url, "http://127.0.0.1:8787/workspaces/grant-lab-local001?token=share_local_secret");
    assert.equal(composeService.environment.OPL_WORKSPACE_ID, "ws-local001");
    assert.equal(composeService.environment.OPL_SHARE_TOKEN, "share_local_secret");
    assert.deepEqual(composeService.volumes, [
      `${join(storage.localPath, "data")}:/data`,
      `${join(storage.localPath, "projects")}:/projects`
    ]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

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
        markup: 0.2
      }
    });

    await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
    const workspace = await service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "Real Docker Lab",
      packageId: "basic"
    });

    const workspaceDir = join(root, workspace.id);
    const composePath = join(workspaceDir, "docker-compose.yml");
    const diskPath = join(workspaceDir, "disk");
    const dataPath = join(diskPath, "data");
    const projectsPath = join(diskPath, "projects");
    const envPath = join(workspaceDir, ".env");

    assert.equal(workspace.provider, "local-docker");
    assert.equal(workspace.server.status, "running");
    assert.equal(workspace.docker.status, "running");
    assert.equal(workspace.disk.status, "attached_retained");
    assert.equal(workspace.disk.localPath, diskPath);
    assert.equal(workspace.url, `http://127.0.0.1:8787/workspaces/${workspace.slug}?token=${workspace.access.token}`);
    assert.equal(await exists(composePath), true);
    assert.equal(await exists(diskPath), true);
    assert.equal(await exists(dataPath), true);
    assert.equal(await exists(projectsPath), true);
    assert.equal(await exists(envPath), true);

    const compose = parse(await readFile(composePath, "utf8"));
    const composeService = Object.values(compose.services)[0];
    assert.equal(composeService.image, "ghcr.io/gaofeng21cn/one-person-lab-app:latest");
    assert.deepEqual(composeService.environment, {
      OPL_WORKSPACE_ID: workspace.id,
      OPL_WORKSPACE_NAME: "Real Docker Lab",
      OPL_SHARE_TOKEN: workspace.access.token,
      OPL_PACKAGE_ID: "basic",
      DATA_DIR: "/data",
      AIONUI_DATA_DIR: "/data",
      OPL_PROJECTS_DIR: "/projects",
      ALLOW_REMOTE: "true",
      OPL_WEBUI_AUTH_MODE: "none",
      HOME: "/data",
      OPL_WORKSPACE_ROOT: "/projects",
      CODEX_HOME: "/data/codex"
    });
    assert.deepEqual(composeService.ports, ["127.0.0.1::3000"]);
    assert.deepEqual(composeService.volumes, ["./disk/data:/data", "./disk/projects:/projects"]);

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
