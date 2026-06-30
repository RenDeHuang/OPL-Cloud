export class TencentCvmProvider {
  constructor({ env = process.env } = {}) {
    this.name = "tencent-cvm";
    this.env = env;
  }

  async createWorkspaceRuntime({ workspaceId, workspaceName, packagePlan, token }) {
    this.requireExecutionBoundary();
    throw new Error([
      "tencent_cvm_provider_requires_runner",
      `workspaceId=${workspaceId}`,
      `workspaceName=${workspaceName}`,
      `packageId=${packagePlan.id}`,
      `token=${token ? "present" : "missing"}`,
      "run infra/tencent-cvm OpenTofu apply, then ansible/workspace.yml, and write outputs back to the API"
    ].join(";"));
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
    const required = [
      "TENCENTCLOUD_SECRET_ID",
      "TENCENTCLOUD_SECRET_KEY",
      "TENCENTCLOUD_REGION",
      "OPL_WORKSPACE_DOMAIN",
      "OPL_VPC_ID",
      "OPL_SUBNET_ID",
      "OPL_SECURITY_GROUP_ID"
    ];
    const missing = required.filter((key) => !this.env[key]);
    if (missing.length > 0) {
      throw new Error(`tencent_cvm_provider_missing_env:${missing.join(",")}`);
    }
  }
}
