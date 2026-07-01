import { access, mkdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), "../../../..");
const REQUIRED_ENV = [
  "OPL_WORKSPACE_DOMAIN",
  "OPL_WORKSPACE_IMAGE",
  "OPL_K8S_NAMESPACE",
  "OPL_INGRESS_CLASS",
  "OPL_IMAGE_PULL_SECRET_NAME",
  "OPL_WORKSPACE_STORAGE_CLASS",
  "TENCENT_DEPLOY_KUBECONFIG_REF"
];
const REQUIRED_TOOLS = ["kubectl"];

function compactId(value) {
  return String(value)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 48);
}

function workspaceSlug(workspaceName, workspaceId) {
  const suffix = compactId(workspaceId).slice(-6);
  return `${compactId(workspaceName)}-${suffix}`.slice(0, 63);
}

function k8sName(workspaceId) {
  return `opl-${compactId(workspaceId)}`.slice(0, 63);
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

function b64(value) {
  return Buffer.from(String(value), "utf8").toString("base64");
}

export class TencentTkeProvider {
  constructor({
    env = process.env,
    runner = defaultRunner,
    commandExists = defaultCommandExists,
    stateRootDir = join(repoRoot, ".runtime", "tencent-tke")
  } = {}) {
    this.name = "tencent-tke";
    this.env = env;
    this.runner = runner;
    this.commandExists = commandExists;
    this.stateRootDir = stateRootDir;
  }

  async createWorkspaceRuntime({ workspaceId, ownerAccountId = "unknown", workspaceName, packagePlan, token }) {
    this.requireExecutionBoundary();
    await this.requireTools(REQUIRED_TOOLS);

    const name = k8sName(workspaceId);
    const slug = workspaceSlug(workspaceName, workspaceId);
    const manifestPath = await this.writeWorkspaceManifest({
      name,
      slug,
      workspaceId,
      ownerAccountId,
      workspaceName,
      packagePlan,
      token
    });
    await this.runKubectl(["apply", "-f", manifestPath]);

    return {
      provider: this.name,
      server: {
        id: `deployment/${name}`,
        status: "running",
        billingStatus: "active",
        spec: packagePlan.server,
        namespace: this.env.OPL_K8S_NAMESPACE
      },
      docker: {
        id: `deployment/${name}`,
        image: this.env.OPL_WORKSPACE_IMAGE,
        status: "running",
        service: `service/${name}`
      },
      disk: {
        id: `pvc/${name}-data`,
        status: "attached_retained",
        billingStatus: "active",
        sizeGb: packagePlan.diskGb,
        mountPath: "/data",
        storageClass: this.env.OPL_WORKSPACE_STORAGE_CLASS
      },
      url: this.workspaceUrl({ workspaceId, token }),
      slug
    };
  }

  workspaceUrl({ workspaceId, token }) {
    const domain = String(this.env.OPL_WORKSPACE_DOMAIN || "").replace(/^https?:\/\//, "").replace(/\/$/, "");
    return `https://${domain}/w/${workspaceId}?token=${token}`;
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
    await this.runKubectl(["scale", workspace.server.id, "--replicas=0"]);
    return {
      ...workspace.server,
      status: "stopped",
      billingStatus: "stopped"
    };
  }

  async restartServer({ workspace }) {
    await this.runKubectl(["scale", workspace.server.id, "--replicas=1"]);
    return {
      ...workspace.server,
      status: "running",
      billingStatus: "active"
    };
  }

  async recreateServer({ workspace }) {
    if (!workspace.disk?.id || workspace.disk.status === "destroyed") {
      throw new Error("retained_storage_required");
    }
    const name = resourceName(workspace.server.id);
    const manifestPath = await this.writeWorkspaceManifest({
      name,
      slug: workspace.slug,
      workspaceId: workspace.id,
      ownerAccountId: workspace.ownerAccountId,
      workspaceName: workspace.name,
      packagePlan: {
        id: workspace.packageId,
        server: workspace.server.spec,
        diskGb: workspace.disk.sizeGb
      },
      token: workspace.access.token
    });
    await this.runKubectl(["apply", "-f", manifestPath]);
    return {
      ...workspace.server,
      status: "running",
      billingStatus: "active"
    };
  }

  async destroyServer({ workspace }) {
    const name = resourceName(workspace.server.id);
    await this.runKubectl([
      "delete",
      `deployment/${name}`,
      `service/${name}`,
      `ingress/${name}`,
      `secret/${name}-env`,
      "--ignore-not-found=true"
    ]);
    return {
      ...workspace.server,
      status: "destroyed",
      billingStatus: "stopped"
    };
  }

  async destroyDisk({ workspace }) {
    await this.runKubectl(["delete", workspace.disk.id, "--ignore-not-found=true"]);
    return {
      ...workspace.disk,
      status: "destroyed",
      billingStatus: "stopped"
    };
  }

  requireExecutionBoundary() {
    const missing = this.missingEnv();
    if (missing.length > 0) {
      throw new Error(`tencent_tke_provider_missing_env:${missing.join(",")}`);
    }
  }

  missingEnv() {
    return REQUIRED_ENV.filter((key) => !this.env[key]);
  }

  async requireTools(commands) {
    const missingTools = [];
    for (const command of commands) {
      if (!(await this.commandExists(command))) missingTools.push(command);
    }
    if (missingTools.length > 0) {
      throw new Error(`tencent_tke_provider_missing_tools:${missingTools.join(",")}`);
    }
  }

  kubectlArgs(args) {
    return [
      "--kubeconfig",
      this.env.TENCENT_DEPLOY_KUBECONFIG_REF,
      "--namespace",
      this.env.OPL_K8S_NAMESPACE,
      ...args
    ];
  }

  async runKubectl(args) {
    this.requireExecutionBoundary();
    await this.requireTools(REQUIRED_TOOLS);
    return this.runner({
      command: "kubectl",
      args: this.kubectlArgs(args),
      cwd: repoRoot,
      env: this.env
    });
  }

  async writeWorkspaceManifest(input) {
    const stateDir = join(this.stateRootDir, compactId(input.workspaceId));
    await mkdir(stateDir, { recursive: true });
    const manifestPath = join(stateDir, "workspace.k8s.json");
    await writeFile(manifestPath, `${JSON.stringify(this.workspaceManifest(input), null, 2)}\n`, { mode: 0o600 });
    return manifestPath;
  }

  workspaceManifest({ name, workspaceId, ownerAccountId, workspaceName, packagePlan, token }) {
    const labels = {
      "app.kubernetes.io/name": "opl-workspace",
      "app.kubernetes.io/instance": name,
      "oplcloud.cn/workspace-id": workspaceId
    };
    const selector = { matchLabels: labels };
    const nodeSelectorKey = this.env.OPL_WORKSPACE_NODE_SELECTOR_KEY;
    const nodeSelectorValue = this.env.OPL_WORKSPACE_NODE_SELECTOR_VALUE;
    return {
      apiVersion: "v1",
      kind: "List",
      items: [
        {
          apiVersion: "v1",
          kind: "Secret",
          metadata: { name: `${name}-env`, labels },
          type: "Opaque",
          data: {
            OPL_SHARE_TOKEN: b64(token)
          }
        },
        {
          apiVersion: "v1",
          kind: "PersistentVolumeClaim",
          metadata: { name: `${name}-data`, labels },
          spec: {
            accessModes: ["ReadWriteOnce"],
            storageClassName: this.env.OPL_WORKSPACE_STORAGE_CLASS,
            resources: { requests: { storage: `${packagePlan.diskGb}Gi` } }
          }
        },
        {
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: { name, labels },
          spec: {
            replicas: 1,
            selector,
            template: {
              metadata: { labels },
              spec: {
                automountServiceAccountToken: false,
                imagePullSecrets: [{ name: this.env.OPL_IMAGE_PULL_SECRET_NAME }],
                nodeSelector: nodeSelectorKey && nodeSelectorValue ? { [nodeSelectorKey]: nodeSelectorValue } : undefined,
                containers: [
                  {
                    name: "workspace",
                    image: this.env.OPL_WORKSPACE_IMAGE,
                    imagePullPolicy: "IfNotPresent",
                    ports: [{ name: "http", containerPort: Number(this.env.OPL_WORKSPACE_WEBUI_PORT || 3000) }],
                    envFrom: [{ secretRef: { name: `${name}-env` } }],
                    env: [
                      { name: "OPL_WORKSPACE_ID", value: workspaceId },
                      { name: "OPL_WORKSPACE_NAME", value: workspaceName },
                      { name: "OPL_OWNER_ACCOUNT_ID", value: ownerAccountId },
                      { name: "OPL_PACKAGE_ID", value: packagePlan.id },
                      { name: "DATA_DIR", value: "/data" },
                      { name: "AIONUI_DATA_DIR", value: "/data" },
                      { name: "OPL_PROJECTS_DIR", value: "/projects" },
                      { name: "ALLOW_REMOTE", value: "true" },
                      { name: "OPL_WEBUI_AUTH_MODE", value: "none" },
                      { name: "HOME", value: "/data" },
                      { name: "OPL_WORKSPACE_ROOT", value: "/projects" },
                      { name: "CODEX_HOME", value: "/data/codex" }
                    ],
                    volumeMounts: [
                      { name: "workspace-data", mountPath: "/data", subPath: "data" },
                      { name: "workspace-data", mountPath: "/projects", subPath: "projects" }
                    ],
                    readinessProbe: {
                      httpGet: { path: "/", port: 3000 },
                      initialDelaySeconds: 10,
                      periodSeconds: 10
                    }
                  }
                ],
                volumes: [
                  { name: "workspace-data", persistentVolumeClaim: { claimName: `${name}-data` } }
                ]
              }
            }
          }
        },
        {
          apiVersion: "v1",
          kind: "Service",
          metadata: { name, labels },
          spec: {
            type: "ClusterIP",
            selector: labels,
            ports: [{ name: "http", port: 3000, targetPort: "http" }]
          }
        },
        {
          apiVersion: "networking.k8s.io/v1",
          kind: "Ingress",
          metadata: {
            name,
            labels,
            annotations: {
              "ingress.cloud.tencent.com/rewrite-support": "true"
            }
          },
          spec: {
            ingressClassName: this.env.OPL_INGRESS_CLASS,
            rules: [
              {
                host: this.env.OPL_WORKSPACE_DOMAIN,
                http: {
                  paths: [
                    {
                      path: `/w/${workspaceId}`,
                      pathType: "Prefix",
                      backend: {
                        service: {
                          name,
                          port: { number: 3000 }
                        }
                      }
                    }
                  ]
                }
              }
            ]
          }
        }
      ]
    };
  }
}

function resourceName(resourceId) {
  return String(resourceId || "").split("/").pop();
}
