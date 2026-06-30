export class LocalDockerProvider {
  constructor() {
    this.name = "local-docker";
  }

  async createWorkspaceRuntime() {
    throw new Error("local_docker_provider_not_configured");
  }

  async stopServer() {
    throw new Error("local_docker_provider_not_configured");
  }

  async restartServer() {
    throw new Error("local_docker_provider_not_configured");
  }

  async destroyServer() {
    throw new Error("local_docker_provider_not_configured");
  }

  async destroyDisk() {
    throw new Error("local_docker_provider_not_configured");
  }
}
