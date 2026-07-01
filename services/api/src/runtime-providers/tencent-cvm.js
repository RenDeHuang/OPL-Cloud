import { spawn } from "node:child_process";
import { access, mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), "../../../..");
const REQUIRED_ENV = [
  "TENCENTCLOUD_SECRET_ID",
  "TENCENTCLOUD_SECRET_KEY",
  "TENCENTCLOUD_REGION",
  "OPL_WORKSPACE_DOMAIN",
  "OPL_VPC_ID",
  "OPL_SUBNET_ID",
  "OPL_SECURITY_GROUP_ID",
  "OPL_AVAILABILITY_ZONE",
  "OPL_IMAGE_ID",
  "OPL_SSH_KEY_ID",
  "OPL_WORKSPACE_IMAGE"
];
const REQUIRED_TOOLS = ["tofu", "ansible-playbook"];

function compactId(value) {
  return String(value)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 48);
}

function workspaceSlug(workspaceName, workspaceId) {
  const suffix = compactId(workspaceId).slice(-7);
  return `${compactId(workspaceName)}-${suffix}`.slice(0, 63);
}

async function defaultRunner({ command, args, cwd, env }) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, env: { ...process.env, ...env }, stdio: "pipe" });
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

async function defaultCommandExists(command) {
  const paths = String(process.env.PATH || "").split(":").filter(Boolean);
  for (const path of paths) {
    try {
      await access(join(path, command));
      return true;
    } catch {
      // Try the next PATH entry.
    }
  }
  return false;
}

function outputValue(outputs, key) {
  const value = outputs?.[key];
  if (value && typeof value === "object" && "value" in value) return value.value;
  return value;
}

export class TencentCvmProvider {
  constructor({
    env = process.env,
    runner = defaultRunner,
    commandExists = defaultCommandExists,
    infraDir = join(repoRoot, "infra", "tencent-cvm"),
    stateRootDir = join(repoRoot, ".runtime", "tencent-cvm")
  } = {}) {
    this.name = "tencent-cvm";
    this.env = env;
    this.runner = runner;
    this.commandExists = commandExists;
    this.infraDir = infraDir;
    this.stateRootDir = stateRootDir;
  }

  async createWorkspaceRuntime({ workspaceId, ownerAccountId = "unknown", workspaceName, packagePlan, token }) {
    this.requireExecutionBoundary();
    await this.requireTools(REQUIRED_TOOLS);

    const slug = workspaceSlug(workspaceName, workspaceId);
    const oplImage = this.env.OPL_WORKSPACE_IMAGE;
    const statePaths = await this.prepareStatePaths(workspaceId);
    const common = {
      cwd: this.infraDir,
      env: {
        ...this.env,
        TF_DATA_DIR: statePaths.dataDir
      }
    };
    const vars = [
      ["workspace_id", workspaceId],
      ["workspace_slug", slug],
      ["workspace_token", token],
      ["workspace_domain", this.env.OPL_WORKSPACE_DOMAIN],
      ["owner_account_id", this.env.OPL_OWNER_ACCOUNT_ID || ownerAccountId],
      ["package_id", packagePlan.id],
      ["opl_image", oplImage],
      ["region", this.env.TENCENTCLOUD_REGION],
      ["availability_zone", this.env.OPL_AVAILABILITY_ZONE],
      ["image_id", this.env.OPL_IMAGE_ID],
      ["vpc_id", this.env.OPL_VPC_ID],
      ["subnet_id", this.env.OPL_SUBNET_ID],
      ["security_group_id", this.env.OPL_SECURITY_GROUP_ID],
      ["key_id", this.env.OPL_SSH_KEY_ID || ""]
    ].flatMap(([key, value]) => ["-var", `${key}=${value}`]);

    await this.runner({ command: "tofu", args: ["init", "-input=false"], ...common });
    await this.runner({
      command: "tofu",
      args: [
        "apply",
        "-auto-approve",
        "-input=false",
        `-state=${statePaths.stateFile}`,
        `-state-out=${statePaths.stateFile}`,
        `-backup=${statePaths.backupFile}`,
        ...vars
      ],
      ...common
    });
    const rawOutputs = await this.runner({
      command: "tofu",
      args: ["output", "-json", `-state=${statePaths.stateFile}`, "-show-sensitive"],
      ...common
    });
    const outputs = JSON.parse(rawOutputs);
    const serverId = outputValue(outputs, "server_id");
    const diskId = outputValue(outputs, "disk_id");
    const publicIp = outputValue(outputs, "public_ip");
    const url = outputValue(outputs, "workspace_url");
    if (!serverId || !diskId || !publicIp || !url) {
      throw new Error("tencent_cvm_provider_incomplete_outputs");
    }

    await this.runner({
      command: "ansible-playbook",
      args: [
        "-i",
        `${publicIp},`,
        "ansible/workspace.yml",
        "-u",
        "root",
        "--extra-vars",
        [
          `workspace_id=${workspaceId}`,
          `workspace_slug=${slug}`,
          `workspace_token=${token}`,
          `workspace_domain=${this.env.OPL_WORKSPACE_DOMAIN}`,
          `opl_image=${oplImage}`
        ].join(" ")
      ],
      ...common
    });

    return {
      provider: this.name,
      server: {
        id: serverId,
        status: "running",
        billingStatus: "active",
        spec: packagePlan.server,
        publicIp
      },
      docker: {
        id: `docker-${workspaceId}`,
        image: oplImage,
        status: "running",
        remoteHost: publicIp
      },
      disk: {
        id: diskId,
        status: "attached_retained",
        billingStatus: "active",
        sizeGb: packagePlan.diskGb,
        mountPath: "/data"
      },
      url,
      slug
    };
  }

  async readiness() {
    const missingEnv = this.missingEnv();
    const missingTools = [];
    for (const command of REQUIRED_TOOLS) {
      if (!(await this.commandExists(command))) missingTools.push(command);
    }
    return {
      provider: this.name,
      ready: missingEnv.length === 0 && missingTools.length === 0,
      missingEnv,
      missingTools
    };
  }

  async stopServer() {
    throw new Error("tencent_cvm_provider_not_configured");
  }

  async restartServer() {
    throw new Error("tencent_cvm_provider_not_configured");
  }

  async destroyServer() {
    throw new Error("tencent_cvm_provider_not_configured");
  }

  async destroyDisk() {
    throw new Error("tencent_cvm_provider_not_configured");
  }

  requireExecutionBoundary() {
    const missing = this.missingEnv();
    if (missing.length > 0) {
      throw new Error(`tencent_cvm_provider_missing_env:${missing.join(",")}`);
    }
  }

  missingEnv() {
    return REQUIRED_ENV.filter((key) => !this.env[key]);
  }

  async prepareStatePaths(workspaceId) {
    const stateDir = join(this.stateRootDir, compactId(workspaceId));
    await mkdir(stateDir, { recursive: true });
    return {
      stateDir,
      dataDir: join(stateDir, ".tofu"),
      stateFile: join(stateDir, "terraform.tfstate"),
      backupFile: join(stateDir, "terraform.tfstate.backup")
    };
  }

  async requireTools(commands) {
    const missing = [];
    for (const command of commands) {
      if (!(await this.commandExists(command))) missing.push(command);
    }
    if (missing.length > 0) {
      throw new Error(`tencent_cvm_provider_missing_tools:${missing.join(",")}`);
    }
  }
}
