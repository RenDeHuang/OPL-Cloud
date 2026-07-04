import { mkdir, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { spawn } from "node:child_process";

function compactId(value) {
  return String(value)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 48);
}

function composeServiceName(workspaceId) {
  return `opl-${workspaceId.replace(/[^a-zA-Z0-9]/g, "-")}`;
}

async function runCommand(command, args, cwd) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, stdio: "pipe" });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      stdout += chunk.toString();
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString();
    });
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) resolve(stdout.trim());
      else reject(new Error(`${command} ${args.join(" ")} failed: ${stderr.trim()}`));
    });
  });
}

export class LocalDockerProvider {
  constructor({
    rootDir = ".runtime/workspaces",
    baseUrl = "http://127.0.0.1:8787",
    image = "ghcr.io/gaofeng21cn/one-person-lab-app:latest",
    execute = process.env.OPL_LOCAL_DOCKER_EXECUTE === "1"
  } = {}) {
    this.name = "local-docker";
    this.rootDir = rootDir;
    this.baseUrl = baseUrl.replace(/\/$/, "");
    this.image = image;
    this.execute = execute;
  }

  async createStorageVolume({ storageId, storage = {}, packagePlan }) {
    const storageDir = join(this.rootDir, storageId);
    const diskPath = join(storageDir, "disk");
    await mkdir(join(diskPath, "data"), { recursive: true });
    await mkdir(join(diskPath, "projects"), { recursive: true });
    return {
      providerResourceId: `local-storage-${storageId}`,
      status: "available",
      billingStatus: "active",
      sizeGb: storage.sizeGb || packagePlan.diskGb,
      localPath: diskPath
    };
  }

  async createComputeAllocation({ computeAllocationId, computeAllocation = {}, packagePlan }) {
    const allocationId = computeAllocationId || computeAllocation.id;
    const computeDir = join(this.rootDir, allocationId);
    await mkdir(computeDir, { recursive: true });
    return {
      providerResourceId: `local-server-${allocationId}`,
      status: "running",
      billingStatus: "active",
      spec: packagePlan.server,
      image: this.image,
      localPath: computeDir,
      composePath: join(computeDir, "docker-compose.yml"),
      runtime: {
        dockerId: `local-docker-${allocationId}`,
        serviceName: composeServiceName(allocationId),
        localPath: computeDir,
        name: computeAllocation.name || allocationId
      }
    };
  }

  async attachStorage({ attachment, compute, storage }) {
    const computeDir = compute.localPath || compute.runtime?.localPath || join(this.rootDir, compute.id);
    const storagePath = storage.localPath || join(this.rootDir, storage.id, "disk");
    await mkdir(computeDir, { recursive: true });
    await mkdir(join(storagePath, "data"), { recursive: true });
    await mkdir(join(storagePath, "projects"), { recursive: true });
    const composePath = join(computeDir, "docker-compose.yml");
    await writeFile(composePath, this.composeFile({
      serviceName: compute.runtime?.serviceName || composeServiceName(compute.id),
      workspaceId: compute.id,
      workspaceName: compute.name || compute.id,
      packagePlan: { id: compute.packageId || "basic" },
      token: "",
      hostDataPath: join(storagePath, "data"),
      hostProjectsPath: join(storagePath, "projects")
    }));
    return {
      providerAttachmentId: `local-attachment-${attachment.id}`,
      status: "attached",
      composePath,
      localPath: computeDir
    };
  }

  async detachStorage({ attachment }) {
    return {
      providerAttachmentId: attachment.providerAttachmentId,
      status: "detached"
    };
  }

  async destroyComputeAllocation({ computeAllocation }) {
    const computeDir = computeAllocation.localPath || computeAllocation.runtime?.localPath;
    if (this.execute && computeDir) {
      await runCommand("docker", ["compose", "down"], computeDir);
    }
    if (computeDir) {
      await rm(computeDir, { recursive: true, force: true });
    }
    return {
      providerResourceId: computeAllocation.providerResourceId,
      status: "destroyed",
      billingStatus: "stopped"
    };
  }

  async destroyStorageVolume({ storage }) {
    if (storage.localPath) {
      await rm(storage.localPath, { recursive: true, force: true });
    }
    return {
      providerResourceId: storage.providerResourceId,
      status: "destroyed",
      billingStatus: "stopped"
    };
  }

  async createWorkspaceEntry({ workspaceId, workspaceName, slug, token, attachment, compute, storage, packagePlan }) {
    const computeDir = compute.localPath || compute.runtime?.localPath || attachment.localPath || join(this.rootDir, compute.id);
    const storagePath = storage.localPath || join(this.rootDir, storage.id, "disk");
    const serviceName = compute.runtime?.serviceName || composeServiceName(compute.id || workspaceId);
    await mkdir(computeDir, { recursive: true });
    await mkdir(join(storagePath, "data"), { recursive: true });
    await mkdir(join(storagePath, "projects"), { recursive: true });
    await writeFile(join(computeDir, ".env"), [
      `OPL_WORKSPACE_ID=${workspaceId}`,
      `OPL_WORKSPACE_NAME=${workspaceName}`,
      `OPL_SHARE_TOKEN=${token}`,
      `OPL_DATA_DIR=${storagePath}`,
      `OPL_PACKAGE_ID=${packagePlan.id}`,
      ""
    ].join("\n"));
    const composePath = join(computeDir, "docker-compose.yml");
    await writeFile(composePath, this.composeFile({
      serviceName,
      workspaceId,
      workspaceName,
      packagePlan,
      token,
      hostDataPath: join(storagePath, "data"),
      hostProjectsPath: join(storagePath, "projects")
    }));
    if (this.execute) {
      await runCommand("docker", ["compose", "up", "-d"], computeDir);
    }
    return {
      provider: this.name,
      slug,
      url: this.workspaceUrl({ slug, token }),
      status: "ready",
      composePath
    };
  }

  workspaceUrl({ slug, token }) {
    return `${this.baseUrl}/workspaces/${slug}?token=${token}`;
  }

  async resolveLocalUrl(workspaceDir, serviceName) {
    const port = await runCommand("docker", ["compose", "port", serviceName, "3000"], workspaceDir);
    const normalized = port.replace(/^0\.0\.0\.0:/, "127.0.0.1:").replace(/^:::/, "127.0.0.1:");
    return `http://${normalized}`;
  }

  composeFile({ serviceName, workspaceId, workspaceName, packagePlan, token, hostDataPath = "./disk/data", hostProjectsPath = "./disk/projects" }) {
    return [
      "services:",
      `  ${serviceName}:`,
      `    image: ${this.image}`,
      "    restart: unless-stopped",
      "    environment:",
      `      OPL_WORKSPACE_ID: ${JSON.stringify(workspaceId)}`,
      `      OPL_WORKSPACE_NAME: ${JSON.stringify(workspaceName)}`,
      `      OPL_SHARE_TOKEN: ${JSON.stringify(token)}`,
      `      OPL_PACKAGE_ID: ${JSON.stringify(packagePlan.id)}`,
      "      DATA_DIR: /data",
      "      AIONUI_DATA_DIR: /data",
      "      OPL_PROJECTS_DIR: /projects",
      "      ALLOW_REMOTE: \"true\"",
      "      OPL_WEBUI_AUTH_MODE: none",
      "      AIONUI_WEBUI_AUTH_MODE: none",
      "      HOME: /data",
      "      OPL_WORKSPACE_ROOT: /projects",
      "      CODEX_HOME: /data/codex",
      "    ports:",
      "      - \"127.0.0.1::3000\"",
      "    volumes:",
      `      - ${hostDataPath}:/data`,
      `      - ${hostProjectsPath}:/projects`,
      ""
    ].join("\n");
  }
}
