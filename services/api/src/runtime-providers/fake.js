function compactId(value) {
  return String(value)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 48);
}

export function workspaceSlug(workspaceName, workspaceId) {
  return `${compactId(workspaceName)}-${workspaceId.slice(-6)}`;
}

export class FakeRuntimeProvider {
  constructor({ baseDomain = "oplcloud.cn" } = {}) {
    this.name = "fake-provider";
    this.baseDomain = baseDomain;
  }

  async createWorkspaceRuntime({ workspaceId, workspaceName, packagePlan, token }) {
    const slug = workspaceSlug(workspaceName, workspaceId);
    return {
      provider: this.name,
      server: {
        id: `server-${workspaceId}`,
        status: "running",
        billingStatus: "active",
        spec: packagePlan.server
      },
      docker: {
        id: `docker-${workspaceId}`,
        image: "one-person-lab-app:latest",
        status: "running"
      },
      disk: {
        id: `disk-${workspaceId}`,
        status: "attached_retained",
        billingStatus: "active",
        sizeGb: packagePlan.diskGb,
        mountPath: "/workspace"
      },
      url: `https://${slug}.${this.baseDomain}/?token=${token}`,
      slug
    };
  }

  async stopServer({ workspace }) {
    return {
      ...workspace.server,
      status: "stopped",
      billingStatus: "stopped"
    };
  }

  async restartServer({ workspace }) {
    return {
      ...workspace.server,
      id: workspace.server.status === "destroyed" ? `server-${workspace.id}-recreated` : workspace.server.id,
      status: "running",
      billingStatus: "active"
    };
  }

  async destroyServer({ workspace }) {
    return {
      ...workspace.server,
      status: "destroyed",
      billingStatus: "stopped"
    };
  }

  async destroyDisk({ workspace }) {
    return {
      ...workspace.disk,
      status: "destroyed",
      billingStatus: "stopped"
    };
  }
}
