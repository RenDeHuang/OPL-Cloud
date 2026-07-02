import { spawn } from "node:child_process";
import { access, mkdir, writeFile } from "node:fs/promises";
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
const REQUIRED_TOOLS = ["tofu", "ansible-playbook", "tccli"];
const PACKAGE_INSTANCE_TYPES = {
  basic: "SA5.MEDIUM4",
  pro: "SA5.2XLARGE16",
  gpu: "GN10Xp.4XLARGE40"
};

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
    const vars = {
      workspace_id: workspaceId,
      workspace_slug: slug,
      workspace_token: token,
      workspace_domain: this.env.OPL_WORKSPACE_DOMAIN,
      owner_account_id: this.env.OPL_OWNER_ACCOUNT_ID || ownerAccountId,
      package_id: packagePlan.id,
      opl_image: oplImage,
      region: this.env.TENCENTCLOUD_REGION,
      availability_zone: this.env.OPL_AVAILABILITY_ZONE,
      image_id: this.env.OPL_IMAGE_ID,
      vpc_id: this.env.OPL_VPC_ID,
      subnet_id: this.env.OPL_SUBNET_ID,
      security_group_id: this.env.OPL_SECURITY_GROUP_ID,
      key_id: this.env.OPL_SSH_KEY_ID || ""
    };
    await this.writeJsonFile(statePaths.tfvarsFile, vars);
    await this.writeJsonFile(statePaths.ansibleVarsFile, this.ansibleVars({
      workspaceId,
      slug,
      token,
      oplImage
    }));

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
        `-var-file=${statePaths.tfvarsFile}`
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

    await this.runAnsibleWorkspace({
      publicIp,
      workspaceId,
      slug,
      token,
      oplImage,
      varsFile: statePaths.ansibleVarsFile,
      cwd: common.cwd,
      env: common.env
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

  async stopServer({ workspace }) {
    await this.runTccli("cvm", "StopInstances", [
      "--InstanceIds",
      JSON.stringify([workspace.server.id]),
      "--StoppedMode",
      "STOP_CHARGING"
    ]);
    return {
      ...workspace.server,
      status: "stopped",
      billingStatus: "stopped"
    };
  }

  async restartServer({ workspace }) {
    await this.runTccli("cvm", "StartInstances", [
      "--InstanceIds",
      JSON.stringify([workspace.server.id])
    ]);
    return {
      ...workspace.server,
      status: "running",
      billingStatus: "active"
    };
  }

  async recreateServer({ workspace }) {
    this.requireExecutionBoundary();
    await this.requireTools(["tccli", "ansible-playbook"]);
    if (!workspace.disk?.id || workspace.disk.status === "destroyed") {
      throw new Error("retained_disk_required");
    }

    const instanceType = PACKAGE_INSTANCE_TYPES[workspace.packageId];
    if (!instanceType) throw new Error("unknown_package");

    const runOutput = await this.runTccli("cvm", "RunInstances", [
      "--Placement",
      JSON.stringify({ Zone: this.env.OPL_AVAILABILITY_ZONE }),
      "--ImageId",
      this.env.OPL_IMAGE_ID,
      "--InstanceType",
      instanceType,
      "--InstanceChargeType",
      "POSTPAID_BY_HOUR",
      "--VirtualPrivateCloud",
      JSON.stringify({ VpcId: this.env.OPL_VPC_ID, SubnetId: this.env.OPL_SUBNET_ID }),
      "--SecurityGroupIds",
      JSON.stringify([this.env.OPL_SECURITY_GROUP_ID]),
      "--InternetAccessible",
      JSON.stringify({ InternetMaxBandwidthOut: 5, PublicIpAssigned: true }),
      "--SystemDisk",
      JSON.stringify({ DiskType: "CLOUD_BSSD", DiskSize: 50 }),
      "--InstanceName",
      `opl-${workspace.slug}`,
      "--LoginSettings",
      JSON.stringify({ KeyIds: [this.env.OPL_SSH_KEY_ID] })
    ]);
    const newInstanceId = this.parseRunInstanceId(runOutput);

    await this.runTccli("cbs", "AttachDisks", [
      "--DiskIds",
      JSON.stringify([workspace.disk.id]),
      "--InstanceId",
      newInstanceId
    ]);

    const describeOutput = await this.runTccli("cvm", "DescribeInstances", [
      "--InstanceIds",
      JSON.stringify([newInstanceId])
    ]);
    const publicIp = this.parseInstancePublicIp(describeOutput, newInstanceId);
    const oplImage = workspace.docker?.image || this.env.OPL_WORKSPACE_IMAGE;
    const statePaths = await this.prepareStatePaths(workspace.id);
    await this.writeJsonFile(statePaths.ansibleVarsFile, this.ansibleVars({
      workspaceId: workspace.id,
      slug: workspace.slug,
      token: workspace.access.token,
      oplImage
    }));

    await this.runAnsibleWorkspace({
      publicIp,
      workspaceId: workspace.id,
      slug: workspace.slug,
      token: workspace.access.token,
      oplImage,
      varsFile: statePaths.ansibleVarsFile,
      cwd: this.infraDir,
      env: this.env
    });

    return {
      ...workspace.server,
      id: newInstanceId,
      status: "running",
      billingStatus: "active",
      publicIp
    };
  }

  async destroyServer({ workspace }) {
    if (workspace.server.status !== "stopped") {
      await this.runTccli("cvm", "StopInstances", [
        "--InstanceIds",
        JSON.stringify([workspace.server.id]),
        "--StoppedMode",
        "STOP_CHARGING"
      ]);
    }
    if (workspace.disk?.id && workspace.disk.status !== "detached_retained" && workspace.disk.status !== "destroyed") {
      await this.runTccli("cbs", "DetachDisks", [
        "--DiskIds",
        JSON.stringify([workspace.disk.id])
      ]);
    }
    await this.runTccli("cvm", "TerminateInstances", [
      "--InstanceIds",
      JSON.stringify([workspace.server.id])
    ]);
    return {
      ...workspace.server,
      status: "destroyed",
      billingStatus: "stopped"
    };
  }

  async destroyDisk({ workspace }) {
    await this.runTccli("cbs", "TerminateDisks", [
      "--DiskIds",
      JSON.stringify([workspace.disk.id])
    ]);
    return {
      ...workspace.disk,
      status: "destroyed",
      billingStatus: "stopped"
    };
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
      backupFile: join(stateDir, "terraform.tfstate.backup"),
      tfvarsFile: join(stateDir, "workspace.auto.tfvars.json"),
      ansibleVarsFile: join(stateDir, "ansible-vars.json")
    };
  }

  async runTccli(service, action, args) {
    this.requireExecutionBoundary();
    await this.requireTools(["tccli"]);
    return this.runner({
      command: "tccli",
      args: [
        service,
        action,
        "--region",
        this.env.TENCENTCLOUD_REGION,
        ...args
      ],
      cwd: this.infraDir,
      env: this.env
    });
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

  async writeJsonFile(path, payload) {
    await writeFile(path, `${JSON.stringify(payload, null, 2)}\n`, { mode: 0o600 });
  }

  ansibleVars({ workspaceId, slug, token, oplImage }) {
    return {
      workspace_id: workspaceId,
      workspace_slug: slug,
      workspace_token: token,
      workspace_domain: this.env.OPL_WORKSPACE_DOMAIN,
      opl_image: oplImage
    };
  }

  async runAnsibleWorkspace({ publicIp, varsFile, cwd, env }) {
    return this.runner({
      command: "ansible-playbook",
      args: [
        "-i",
        `${publicIp},`,
        "ansible/workspace.yml",
        "-u",
        "root",
        "--extra-vars",
        `@${varsFile}`
      ],
      cwd,
      env
    });
  }

  parseRunInstanceId(output) {
    const parsed = JSON.parse(output || "{}");
    const instanceId = parsed.InstanceIdSet?.[0];
    if (!instanceId) throw new Error("tencent_cvm_provider_missing_instance_id");
    return instanceId;
  }

  parseInstancePublicIp(output, instanceId) {
    const parsed = JSON.parse(output || "{}");
    const instance = parsed.InstanceSet?.find((item) => item.InstanceId === instanceId) || parsed.InstanceSet?.[0];
    const publicIp = instance?.PublicIpAddresses?.[0];
    if (!publicIp) throw new Error("tencent_cvm_provider_missing_public_ip");
    return publicIp;
  }
}
