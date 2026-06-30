export class TencentCvmProvider {
  constructor() {
    this.name = "tencent-cvm";
  }

  async createWorkspaceRuntime() {
    throw new Error("tencent_cvm_provider_not_configured");
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
}
