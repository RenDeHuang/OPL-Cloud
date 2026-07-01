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
const SHARED_INGRESS_NAME = "opl-cloud";
const WORKSPACE_ROUTE_MANIFEST = "shared-ingress-route.k8s.json";
const DEFAULT_WORKSPACE_READY_TIMEOUT_MS = 300000;
const DEFAULT_WORKSPACE_READY_POLL_MS = 5000;

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
    try {
      await this.runKubectl(["apply", "-f", manifestPath]);
      await this.addWorkspaceRoute({ workspaceId, serviceName: name });
      await this.waitForWorkspaceRuntimeReady({
        workspace: this.runtimeFixture({
          name,
          workspaceId,
          ownerAccountId,
          workspaceName,
          packagePlan,
          token,
          slug
        })
      });
    } catch (error) {
      await this.cleanupWorkspaceRoute({ workspaceId });
      await this.cleanupCreatedWorkspaceResources({ name, deleteStorage: true });
      throw error;
    }

    return this.runtimeFixture({
      name,
      workspaceId,
      ownerAccountId,
      workspaceName,
      packagePlan,
      token,
      slug
    });
  }

  runtimeFixture({ name, workspaceId, packagePlan, token, slug }) {
    return {
      id: workspaceId,
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
    await this.waitForWorkspaceRuntimeReady({ workspace });
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
    await this.addWorkspaceRoute({ workspaceId: workspace.id, serviceName: name });
    await this.waitForWorkspaceRuntimeReady({
      workspace: {
        ...workspace,
        server: {
          ...workspace.server,
          id: `deployment/${name}`,
          status: "running",
          billingStatus: "active"
        },
        docker: {
          ...workspace.docker,
          id: `deployment/${name}`,
          image: this.env.OPL_WORKSPACE_IMAGE,
          status: "running",
          service: `service/${name}`
        },
        disk: {
          ...workspace.disk,
          id: `pvc/${name}-data`,
          status: "attached_retained"
        }
      }
    });
    return {
      ...workspace.server,
      status: "running",
      billingStatus: "active"
    };
  }

  async destroyServer({ workspace }) {
    const name = resourceName(workspace.server.id);
    let routeCleanupError = null;
    try {
      await this.removeWorkspaceRoute({ workspaceId: workspace.id });
    } catch (error) {
      routeCleanupError = error;
    }
    await this.runKubectl([
      "delete",
      `deployment/${name}`,
      `service/${name}`,
      `secret/${name}-env`,
      "--ignore-not-found=true"
    ]);
    return {
      ...workspace.server,
      status: "destroyed",
      billingStatus: "stopped",
      ...(routeCleanupError
        ? {
          routeCleanupStatus: "failed",
          routeCleanupError: routeCleanupError.message
        }
        : {})
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

  async runtimeStatus({ workspace }) {
    const name = resourceName(workspace.server.id);
    const pvcName = resourceName(workspace.disk.id);
    const serviceName = resourceName(workspace.docker.service || `service/${name}`);
    const raw = await this.runKubectl([
      "get",
      `deployment/${name}`,
      `pvc/${pvcName}`,
      `service/${serviceName}`,
      `ingress/${SHARED_INGRESS_NAME}`,
      `endpoints/${serviceName}`,
      "-o",
      "json"
    ]);
    const list = JSON.parse(raw);
    const items = Array.isArray(list.items) ? list.items : [list];
    const deployment = findKubernetesItem(items, "Deployment", name);
    const pvc = findKubernetesItem(items, "PersistentVolumeClaim", pvcName);
    const service = findKubernetesItem(items, "Service", serviceName);
    const ingress = findKubernetesItem(items, "Ingress", SHARED_INGRESS_NAME);
    const endpoints = findKubernetesItem(items, "Endpoints", serviceName);
    const podLabels = deployment?.spec?.template?.metadata?.labels || {};
    const selector = service?.spec?.selector || {};
    const container = (deployment?.spec?.template?.spec?.containers || []).find((item) => item.name === "workspace") ||
      deployment?.spec?.template?.spec?.containers?.[0];
    const deploymentPvc = (deployment?.spec?.template?.spec?.volumes || [])
      .find((volume) => volume.persistentVolumeClaim?.claimName === pvcName);
    const ingressPath = findIngressPath({ ingress, host: this.workspaceHost(), path: `/w/${workspace.id}` });
    const readyAddresses = (endpoints?.subsets || []).reduce((count, subset) => count + (subset.addresses || []).length, 0);
    const deploymentReady = Number(deployment?.status?.readyReplicas || 0) > 0 &&
      Number(deployment?.status?.availableReplicas || 0) > 0;
    const checks = [
      { name: "deployment_ready", ok: deploymentReady },
      { name: "workspace_image_pulled", ok: deploymentReady && container?.image === workspace.docker.image },
      { name: "pvc_bound", ok: pvc?.status?.phase === "Bound" },
      { name: "deployment_uses_retained_pvc", ok: Boolean(deploymentPvc) },
      { name: "service_targets_workspace", ok: selectorMatchesLabels(selector, podLabels) },
      { name: "service_endpoints_ready", ok: readyAddresses > 0 },
      {
        name: "ingress_routes_workspace_url",
        ok: Boolean(
          ingressPath &&
          ingressPath.backend?.service?.name === serviceName &&
          Number(ingressPath.backend?.service?.port?.number) === 3000
        )
      }
    ];

    return {
      provider: this.name,
      workspaceId: workspace.id,
      ready: checks.every((check) => check.ok),
      checks,
      resources: {
        deployment: {
          name,
          readyReplicas: Number(deployment?.status?.readyReplicas || 0),
          availableReplicas: Number(deployment?.status?.availableReplicas || 0),
          image: container?.image || ""
        },
        pvc: {
          name: pvcName,
          phase: pvc?.status?.phase || "Missing",
          storageClass: pvc?.spec?.storageClassName || ""
        },
        service: {
          name: serviceName,
          selector
        },
        ingress: {
          name: SHARED_INGRESS_NAME,
          host: this.workspaceHost(),
          path: ingressPath?.path || ""
        },
        endpoints: {
          name: serviceName,
          readyAddresses
        }
      }
    };
  }

  workspaceHost() {
    return String(this.env.OPL_WORKSPACE_DOMAIN || "").replace(/^https?:\/\//, "").replace(/\/$/, "");
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

  async writeSharedIngressRouteManifest({ workspaceId, ingress }) {
    const stateDir = join(this.stateRootDir, compactId(workspaceId));
    await mkdir(stateDir, { recursive: true });
    const manifestPath = join(stateDir, WORKSPACE_ROUTE_MANIFEST);
    await writeFile(manifestPath, `${JSON.stringify(ingress, null, 2)}\n`, { mode: 0o600 });
    return manifestPath;
  }

  async readSharedIngress() {
    const raw = await this.runKubectl(["get", `ingress/${SHARED_INGRESS_NAME}`, "-o", "json"]);
    return JSON.parse(raw);
  }

  async applySharedIngressRoute({ workspaceId, mutate }) {
    const ingress = await this.readSharedIngress();
    const nextIngress = mutateSharedIngressRoute({
      ingress,
      host: this.workspaceHost(),
      workspaceId,
      mutate
    });
    const manifestPath = await this.writeSharedIngressRouteManifest({ workspaceId, ingress: nextIngress });
    await this.runKubectl(["apply", "-f", manifestPath]);
  }

  async addWorkspaceRoute({ workspaceId, serviceName }) {
    await this.applySharedIngressRoute({
      workspaceId,
      mutate: (paths, routePath) => [
        workspaceIngressPath({ path: routePath, serviceName }),
        ...paths.filter((candidate) => candidate.path !== routePath)
      ]
    });
  }

  async removeWorkspaceRoute({ workspaceId }) {
    await this.applySharedIngressRoute({
      workspaceId,
      mutate: (paths, routePath) => paths.filter((candidate) => candidate.path !== routePath)
    });
  }

  async cleanupCreatedWorkspaceResources({ name, deleteStorage }) {
    await this.runKubectl([
      "delete",
      `deployment/${name}`,
      `service/${name}`,
      `secret/${name}-env`,
      ...(deleteStorage ? [`pvc/${name}-data`] : []),
      "--ignore-not-found=true"
    ]);
  }

  async cleanupWorkspaceRoute({ workspaceId }) {
    try {
      await this.removeWorkspaceRoute({ workspaceId });
    } catch {
      // Best-effort cleanup. The create failure path must still remove compute and storage.
    }
  }

  async waitForWorkspaceRuntimeReady({ workspace }) {
    const deadline = Date.now() + workspaceReadyTimeoutMs(this.env);
    let status = await this.runtimeStatus({ workspace });
    while (!status.ready && Date.now() < deadline) {
      await delay(workspaceReadyPollMs(this.env));
      status = await this.runtimeStatus({ workspace });
    }
    if (!status.ready) {
      const failedChecks = status.checks
        .filter((check) => !check.ok)
        .map((check) => check.name)
        .join(",");
      throw new Error(`tencent_tke_workspace_not_ready:${failedChecks}`);
    }
    return status;
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
                      { name: "OPL_ACCELERATOR", value: packagePlan.accelerator || "cpu" },
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
                    resources: workspaceContainerResources(packagePlan),
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
        }
      ]
    };
  }
}

function resourceName(resourceId) {
  return String(resourceId || "").split("/").pop();
}

function workspaceContainerResources(packagePlan) {
  const cpu = packagePlan.cpu ? String(packagePlan.cpu) : undefined;
  const memory = packagePlan.memoryGb ? `${packagePlan.memoryGb}Gi` : undefined;
  const requests = {};
  const limits = {};
  if (cpu) {
    requests.cpu = cpu;
    limits.cpu = cpu;
  }
  if (memory) {
    requests.memory = memory;
    limits.memory = memory;
  }
  return Object.keys(requests).length ? { requests, limits } : undefined;
}

function workspaceReadyTimeoutMs(env) {
  const value = Number(env.OPL_TKE_WORKSPACE_READY_TIMEOUT_MS);
  return Number.isFinite(value) && value >= 0 ? value : DEFAULT_WORKSPACE_READY_TIMEOUT_MS;
}

function workspaceReadyPollMs(env) {
  const value = Number(env.OPL_TKE_WORKSPACE_READY_POLL_MS);
  return Number.isFinite(value) && value > 0 ? value : DEFAULT_WORKSPACE_READY_POLL_MS;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function findKubernetesItem(items, kind, name) {
  return items.find((item) => item.kind === kind && item.metadata?.name === name);
}

function selectorMatchesLabels(selector, labels) {
  const entries = Object.entries(selector || {});
  return entries.length > 0 && entries.every(([key, value]) => labels?.[key] === value);
}

function findIngressPath({ ingress, host, path }) {
  for (const rule of ingress?.spec?.rules || []) {
    if (rule.host !== host) continue;
    for (const candidate of rule.http?.paths || []) {
      if (candidate.path === path) return candidate;
    }
  }
  return null;
}

function mutateSharedIngressRoute({ ingress, host, workspaceId, mutate }) {
  const next = sanitizeKubernetesApplyManifest(ingress);
  const rule = ensureIngressRule(next, host);
  const routePath = `/w/${workspaceId}`;
  const paths = Array.isArray(rule.http?.paths) ? rule.http.paths : [];
  rule.http ??= {};
  rule.http.paths = sortWorkspacePaths(mutate(paths, routePath));
  return next;
}

function sanitizeKubernetesApplyManifest(item) {
  const next = JSON.parse(JSON.stringify(item));
  delete next.status;
  if (next.metadata) {
    delete next.metadata.creationTimestamp;
    delete next.metadata.generation;
    delete next.metadata.managedFields;
    delete next.metadata.resourceVersion;
    delete next.metadata.uid;
  }
  return next;
}

function ensureIngressRule(ingress, host) {
  ingress.spec ??= {};
  ingress.spec.rules ??= [];
  let rule = ingress.spec.rules.find((candidate) => candidate.host === host);
  if (!rule) {
    rule = { host, http: { paths: [] } };
    ingress.spec.rules.push(rule);
  }
  rule.http ??= {};
  rule.http.paths ??= [];
  return rule;
}

function sortWorkspacePaths(paths) {
  return [...paths].sort((a, b) => {
    if (a.path === "/") return 1;
    if (b.path === "/") return -1;
    return String(a.path).localeCompare(String(b.path));
  });
}

function workspaceIngressPath({ path, serviceName }) {
  return {
    path,
    pathType: "Prefix",
    backend: {
      service: {
        name: serviceName,
        port: { number: 3000 }
      }
    }
  };
}
