import { spawn } from "node:child_process";

function boolFromEnv(value) {
  return String(value || "").toLowerCase() === "true";
}

function createProvisionerError(response) {
  const error = new Error(response.errorCode || "tencent_provisioner_failed");
  error.safeMessage = response.message || error.message;
  error.providerRequestId = response.providerRequestId || "";
  error.retryable = Boolean(response.retryable);
  error.providerData = response.providerData || {};
  error.missingEnv = response.missingEnv || [];
  return error;
}

export class TencentProvisionerClient {
  constructor({
    binPath,
    args = [],
    env = process.env,
    dryRun = boolFromEnv(env.OPL_TENCENT_PROVISIONER_DRY_RUN),
    spawnProcess = spawn
  } = {}) {
    this.binPath = binPath || env.OPL_TENCENT_PROVISIONER_BIN || "opl-tencent-provisioner";
    this.args = args;
    this.env = env;
    this.dryRun = dryRun;
    this.spawnProcess = spawnProcess;
  }

  async readiness() {
    return this.invoke({ action: "readiness" });
  }

  async createComputeAllocation(input) {
    return this.invoke({
      action: "create_compute_allocation",
      dryRun: this.dryRun,
      accountId: input.accountId,
      userId: input.userId || "",
      packageId: input.packageId,
      pool: input.pool,
      allocation: input.allocation
    });
  }

  async destroyComputeAllocation(input) {
    return this.invoke({
      action: "destroy_compute_allocation",
      dryRun: this.dryRun,
      accountId: input.accountId,
      pool: input.pool || {},
      allocation: input.allocation
    });
  }

  async invoke(payload) {
    const response = await this.runProcess(payload);
    if (!response.ok) throw createProvisionerError(response);
    return response;
  }

  async runProcess(payload) {
    return new Promise((resolve, reject) => {
      const child = this.spawnProcess(this.binPath, this.args, {
        env: { ...process.env, ...this.env },
        stdio: ["pipe", "pipe", "pipe"]
      });
      let stdout = "";
      let stderr = "";
      child.stdout.on("data", (chunk) => {
        stdout += chunk.toString();
      });
      child.stderr.on("data", (chunk) => {
        stderr += chunk.toString();
      });
      child.on("error", reject);
      child.on("close", () => {
        try {
          const parsed = JSON.parse(stdout || "{}");
          resolve(parsed);
        } catch (error) {
          error.message = `invalid_tencent_provisioner_output:${error.message}:${stderr.trim()}`;
          reject(error);
        }
      });
      child.stdin.end(`${JSON.stringify(payload)}\n`);
    });
  }
}
