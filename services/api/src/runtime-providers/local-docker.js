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

function workspaceSlug(workspaceName, workspaceId) {
  return `${compactId(workspaceName)}-${workspaceId.slice(-6)}`;
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
    image = "ghcr.io/gaofeng21cn/one-person-lab-webui:latest",
    execute = process.env.OPL_LOCAL_DOCKER_EXECUTE === "1"
  } = {}) {
    this.name = "local-docker";
    this.rootDir = rootDir;
    this.baseUrl = baseUrl.replace(/\/$/, "");
    this.image = image;
    this.execute = execute;
  }

  async createWorkspaceRuntime({ workspaceId, workspaceName, packagePlan, token }) {
    const workspaceDir = join(this.rootDir, workspaceId);
    const diskPath = join(workspaceDir, "disk");
    const slug = workspaceSlug(workspaceName, workspaceId);
    const serviceName = composeServiceName(workspaceId);

    await mkdir(diskPath, { recursive: true });
    await writeFile(join(workspaceDir, ".env"), [
      `OPL_WORKSPACE_ID=${workspaceId}`,
      `OPL_WORKSPACE_NAME=${workspaceName}`,
      `OPL_SHARE_TOKEN=${token}`,
      `OPL_DATA_DIR=${diskPath}`,
      `OPL_PACKAGE_ID=${packagePlan.id}`,
      ""
    ].join("\n"));
    await writeFile(join(workspaceDir, "docker-compose.yml"), this.composeFile({
      serviceName,
      workspaceId,
      workspaceName,
      packagePlan,
      token
    }));

    if (this.execute) {
      await runCommand("docker", ["compose", "up", "-d"], workspaceDir);
    }
    const localUrl = this.execute ? await this.resolveLocalUrl(workspaceDir, serviceName) : null;

    return {
      provider: this.name,
      server: {
        id: `local-server-${workspaceId}`,
        status: "running",
        billingStatus: "active",
        spec: packagePlan.server,
        localPath: workspaceDir
      },
      docker: {
        id: `local-docker-${workspaceId}`,
        image: this.image,
        status: "running",
        composePath: join(workspaceDir, "docker-compose.yml"),
        localUrl
      },
      disk: {
        id: `local-disk-${workspaceId}`,
        status: "attached_retained",
        billingStatus: "active",
        sizeGb: packagePlan.diskGb,
        mountPath: "/data",
        localPath: diskPath
      },
      url: this.workspaceUrl({ slug, token }),
      slug
    };
  }

  workspaceUrl({ slug, token }) {
    return `${this.baseUrl}/workspaces/${slug}?token=${token}`;
  }

  async stopServer({ workspace }) {
    if (this.execute) {
      await runCommand("docker", ["compose", "stop"], workspace.server.localPath);
    }
    return {
      ...workspace.server,
      status: "stopped",
      billingStatus: "stopped"
    };
  }

  async restartServer({ workspace }) {
    if (this.execute) {
      await runCommand("docker", ["compose", "up", "-d"], workspace.server.localPath);
    }
    return {
      ...workspace.server,
      status: "running",
      billingStatus: "active"
    };
  }

  async destroyServer({ workspace }) {
    if (this.execute) {
      await runCommand("docker", ["compose", "down"], workspace.server.localPath);
    }
    return {
      ...workspace.server,
      status: "destroyed",
      billingStatus: "stopped"
    };
  }

  async destroyDisk({ workspace }) {
    if (workspace.disk.localPath) {
      await rm(workspace.disk.localPath, { recursive: true, force: true });
    }
    return {
      ...workspace.disk,
      status: "destroyed",
      billingStatus: "stopped"
    };
  }

  async resolveLocalUrl(workspaceDir, serviceName) {
    const port = await runCommand("docker", ["compose", "port", serviceName, "3000"], workspaceDir);
    const normalized = port.replace(/^0\.0\.0\.0:/, "127.0.0.1:").replace(/^:::/, "127.0.0.1:");
    return `http://${normalized}`;
  }

  composeFile({ serviceName, workspaceId, workspaceName, packagePlan, token }) {
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
      "      ALLOW_REMOTE: \"true\"",
      "      OPL_WEBUI_AUTH_MODE: none",
      "      HOME: /data",
      "      OPL_WORKSPACE_ROOT: /data/workspaces",
      "      CODEX_HOME: /data/codex",
      "    ports:",
      "      - \"127.0.0.1::3000\"",
      "    volumes:",
      "      - ./disk:/data",
      ""
    ].join("\n");
  }
}
