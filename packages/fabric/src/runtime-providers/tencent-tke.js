import { access, mkdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), "../../../..");
const REQUIRED_ENV = [
  "OPL_WORKSPACE_DOMAIN",
  "OPL_WORKSPACE_IMAGE",
  "OPL_K8S_NAMESPACE",
  "OPL_INGRESS_CLASS",
  "OPL_IMAGE_PULL_SECRET_NAME",
  "OPL_WORKSPACE_STORAGE_CLASS",
  "TENCENT_DEPLOY_KUBECONFIG_REF",
  "TENCENT_DEPLOY_CLUSTER_ID",
  "TENCENT_TKE_REGION",
  "TENCENT_MUTATION_SECRET_ID",
  "TENCENT_MUTATION_SECRET_KEY",
  "OPL_TKE_NODEPOOL_AUTOSCALING_GROUP_PARA_JSON",
  "OPL_TKE_NODEPOOL_LAUNCH_CONFIGURE_PARA_JSON"
];
const REQUIRED_TOOLS = ["kubectl", "tccli"];
const SHARED_INGRESS_NAME = "opl-cloud";
const WORKSPACE_GATEWAY_SERVICE_NAME = "opl-cloud-control-plane";
const WORKSPACE_GATEWAY_SERVICE_PORT = 8787;
const WORKSPACE_ROUTE_MANIFEST = "shared-ingress-route.k8s.json";
const WORKSPACE_CODEX_SECRET_KEYS = [
  "OPL_CODEX_MODEL",
  "OPL_CODEX_REASONING_EFFORT",
  "OPL_CODEX_BASE_URL",
  "OPL_CODEX_API_KEY"
];
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

function computeNodeSelector(computeId) {
  return { "oplcloud.cn/compute-id": compactId(computeId) };
}

async function defaultRunner({ command, args, cwd, env }) {
  if (env?.HOME) await mkdir(env.HOME, { recursive: true });
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

  async createStorageVolume({ storageId, accountId = "unknown", storage = {}, packagePlan }) {
    this.requireExecutionBoundary();
    await this.requireTools(REQUIRED_TOOLS);
    const name = k8sName(storageId);
    const manifestPath = await this.writeStorageVolumeManifest({
      storageId,
      accountId,
      name,
      storage,
      packagePlan
    });
    await this.runKubectl(["apply", "-f", manifestPath]);
    return {
      providerResourceId: `pvc/${name}-data`,
      status: "available",
      billingStatus: "active",
      sizeGb: storage.sizeGb || packagePlan.diskGb,
      storageClass: this.env.OPL_WORKSPACE_STORAGE_CLASS
    };
  }

  async createComputeResource({ computeId, accountId = "unknown", compute = {}, packagePlan }) {
    this.requireExecutionBoundary();
    await this.requireTools(REQUIRED_TOOLS);
    const name = k8sName(computeId);
    const existingNodePoolId = nodePoolIdFromCompute(compute);
    const desiredNodes = computeDesiredNodes({ compute, packagePlan });
    const createInput = this.nodePoolCreateInput({
      computeId,
      accountId,
      compute,
      name,
      packagePlan,
      desiredNodes
    });
    let nodePoolId = existingNodePoolId;
    if (nodePoolId) {
      await this.runTccli([
        "tke",
        "ModifyNodePoolDesiredCapacityAboutAsg",
        "--ClusterId",
        this.env.TENCENT_DEPLOY_CLUSTER_ID,
        "--NodePoolId",
        nodePoolId,
        "--DesiredCapacity",
        String(desiredNodes)
      ]);
    } else {
      const created = await this.runTccli([
        "tke",
        "CreateClusterNodePool",
        "--cli-input-json",
        JSON.stringify(createInput)
      ]);
      nodePoolId = created?.Response?.NodePoolId;
      if (!nodePoolId) throw new Error("tencent_tke_nodepool_create_missing_id");
    }
    const detail = await this.waitForNodePoolReady(nodePoolId, desiredNodes);
    const readyNodes = nodePoolReadyNodes(detail);
    return {
      providerResourceId: `nodepool/${nodePoolId}`,
      nodePoolId,
      status: readyNodes >= desiredNodes ? "running" : "provisioning",
      billingStatus: "active",
      spec: packagePlan.server,
      instanceType: packageInstanceType(packagePlan),
      desiredNodes,
      readyNodes,
      image: this.env.OPL_WORKSPACE_IMAGE,
      nodeSelector: computeNodeSelector(computeId),
      nodePool: detail,
      runtime: {
        workloadName: name,
        serviceName: name,
        service: `service/${name}`,
        dockerId: `deployment/${name}`,
        nodeSelector: computeNodeSelector(computeId)
      }
    };
  }

  async attachStorage({ attachment, compute, storage }) {
    this.requireExecutionBoundary();
    await this.requireTools(REQUIRED_TOOLS);
    const computeName = runtimeWorkloadName(compute, attachment.computeId);
    const storageClaimName = resourceName(storage.providerResourceId || storage.id || `pvc/${k8sName(attachment.storageId)}-data`);
    const manifestPath = await this.writeAttachmentManifest({
      attachment,
      compute,
      storage,
      computeName,
      storageClaimName
    });
    await this.runKubectl(["apply", "-f", manifestPath]);
    return {
      providerAttachmentId: `deployment/${computeName}:pvc/${storageClaimName}:${attachment.mountPath || "/data"}`,
      status: "attached",
      computeStatus: "running",
      storageStatus: "attached"
    };
  }

  async detachStorage({ attachment }) {
    this.requireExecutionBoundary();
    await this.requireTools(REQUIRED_TOOLS);
    const computeName = computeNameFromAttachment(attachment);
    let routeCleanupError = null;
    try {
      await this.removeWorkspaceRoutesForService({ serviceName: computeName });
    } catch (error) {
      routeCleanupError = error;
    }
    await this.runKubectl([
      "delete",
      `deployment/${computeName}`,
      `service/${computeName}`,
      `secret/${computeName}-env`,
      "--ignore-not-found=true"
    ]);
    return {
      providerAttachmentId: attachment.providerAttachmentId,
      status: "detached",
      ...(routeCleanupError
        ? {
          routeCleanupStatus: "failed",
          routeCleanupError: routeCleanupError.message
        }
        : {})
    };
  }

  async destroyComputeResource({ compute }) {
    this.requireExecutionBoundary();
    await this.requireTools(REQUIRED_TOOLS);
    const nodePoolId = nodePoolIdFromCompute(compute);
    if (!nodePoolId) {
      throw new Error("tencent_tke_nodepool_id_required");
    }
    const runtimeName = runtimeWorkloadName(compute, compute.id);
    await this.runKubectl([
      "delete",
      `deployment/${runtimeName}`,
      `service/${runtimeName}`,
      `secret/${runtimeName}-env`,
      "--ignore-not-found=true"
    ]);
    await this.runTccli([
      "tke",
      "DeleteClusterNodePool",
      "--ClusterId",
      this.env.TENCENT_DEPLOY_CLUSTER_ID,
      "--NodePoolIds",
      JSON.stringify([nodePoolId])
    ]);
    return {
      providerResourceId: `nodepool/${nodePoolId}`,
      nodePoolId,
      status: "destroyed",
      billingStatus: "closed"
    };
  }

  async destroyStorageVolume({ storage }) {
    this.requireExecutionBoundary();
    await this.requireTools(REQUIRED_TOOLS);
    const name = resourceName(storage.providerResourceId || `pvc/${k8sName(storage.id)}-data`);
    await this.runKubectl(["delete", `pvc/${name}`, "--ignore-not-found=true"]);
    return {
      providerResourceId: `pvc/${name}`,
      status: "destroyed",
      billingStatus: "closed"
    };
  }

  async createWorkspaceEntry({ workspaceId, ownerAccountId = "unknown", workspaceName, token, compute, packagePlan }) {
    this.requireExecutionBoundary();
    await this.requireTools(REQUIRED_TOOLS);
    const computeName = runtimeWorkloadName(compute, compute.id || workspaceId);
    const secretPath = await this.writeWorkspaceEntrySecretManifest({
      workspaceId,
      workspaceName,
      ownerAccountId,
      computeName,
      packagePlan,
      token
    });
    await this.runKubectl(["apply", "-f", secretPath]);
    return {
      provider: this.name,
      slug: workspaceSlug(workspaceName, workspaceId),
      url: this.workspaceUrl({ workspaceId, token }),
      status: "ready"
    };
  }

  workspaceUrl({ workspaceId, token }) {
    const domain = String(this.env.OPL_WORKSPACE_DOMAIN || "").replace(/^https?:\/\//, "").replace(/\/$/, "");
    return `https://${domain}/w/${workspaceId}/?token=${token}`;
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

  async runtimeStatus({ workspace }) {
    const name = runtimeDeploymentName(workspace);
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
    const ingressPath = findIngressPath({ ingress, host: this.workspaceHost(), path: "/" });
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
        name: "ingress_routes_workspace_gateway",
        ok: Boolean(
          ingressPath &&
          ingressPath.backend?.service?.name === WORKSPACE_GATEWAY_SERVICE_NAME &&
          Number(ingressPath.backend?.service?.port?.number) === WORKSPACE_GATEWAY_SERVICE_PORT
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

  async tccliArgs(args) {
    const normalized = [];
    for (let index = 0; index < args.length; index += 1) {
      const value = args[index];
      if (value === "--cli-input-json" && args[index + 1] && !String(args[index + 1]).startsWith("file://")) {
        const inputDir = join(this.stateRootDir, "tccli-input");
        await mkdir(inputDir, { recursive: true });
        const inputPath = join(inputDir, `${Date.now()}-${randomUUID()}.json`);
        await writeFile(inputPath, String(args[index + 1]), "utf8");
        normalized.push(value, `file://${inputPath}`);
        index += 1;
      } else {
        normalized.push(value);
      }
    }
    return [
      ...normalized,
      "--region",
      this.env.TENCENT_TKE_REGION
    ];
  }

  async runTccli(args) {
    this.requireExecutionBoundary();
    await this.requireTools(REQUIRED_TOOLS);
    const cliHome = this.env.OPL_TKE_CLI_HOME || "/tmp/opl-cloud-cli";
    const raw = await this.runner({
      command: "tccli",
      args: await this.tccliArgs(args),
      cwd: repoRoot,
      env: {
        ...this.env,
        HOME: cliHome,
        XDG_CACHE_HOME: join(cliHome, ".cache"),
        XDG_CONFIG_HOME: join(cliHome, ".config"),
        TENCENTCLOUD_SECRET_ID: this.env.TENCENT_MUTATION_SECRET_ID,
        TENCENTCLOUD_SECRET_KEY: this.env.TENCENT_MUTATION_SECRET_KEY
      }
    });
    if (!raw) return {};
    return JSON.parse(raw);
  }

  async describeNodePool(nodePoolId) {
    const response = await this.runTccli([
      "tke",
      "DescribeClusterNodePoolDetail",
      "--ClusterId",
      this.env.TENCENT_DEPLOY_CLUSTER_ID,
      "--NodePoolId",
      nodePoolId
    ]);
    return response?.Response?.NodePool || {};
  }

  async waitForNodePoolReady(nodePoolId, desiredNodes) {
    const timeoutMs = nodePoolReadyTimeoutMs(this.env);
    const pollMs = nodePoolReadyPollMs(this.env);
    const deadline = Date.now() + timeoutMs;
    let detail = await this.describeNodePool(nodePoolId);
    while (timeoutMs > 0 && nodePoolReadyNodes(detail) < desiredNodes && Date.now() < deadline) {
      await delay(pollMs);
      detail = await this.describeNodePool(nodePoolId);
    }
    return detail;
  }

  nodePoolCreateInput({ computeId, accountId, compute, name, packagePlan, desiredNodes }) {
    const replacements = nodePoolTemplateValues({
      computeId,
      accountId,
      name,
      packagePlan,
      desiredNodes,
      compute
    });
    const autoscalingGroup = renderJsonTemplate(this.env.OPL_TKE_NODEPOOL_AUTOSCALING_GROUP_PARA_JSON, replacements);
    const launchConfigure = renderJsonTemplate(this.env.OPL_TKE_NODEPOOL_LAUNCH_CONFIGURE_PARA_JSON, replacements);
    autoscalingGroup.DesiredCapacity ??= desiredNodes;
    autoscalingGroup.MaxSize ??= desiredNodes;
    autoscalingGroup.MinSize ??= 0;
    launchConfigure.InstanceChargeType ??= this.env.OPL_TKE_INSTANCE_CHARGE_TYPE || "POSTPAID_BY_HOUR";
    const labels = nodePoolLabels({ computeId, accountId });
    return {
      ClusterId: this.env.TENCENT_DEPLOY_CLUSTER_ID,
      AutoScalingGroupPara: JSON.stringify(autoscalingGroup),
      LaunchConfigurePara: JSON.stringify(launchConfigure),
      InstanceAdvancedSettings: {
        DesiredPodNumber: Number(this.env.OPL_TKE_NODEPOOL_DESIRED_POD_NUMBER || 64),
        Labels: labels,
        DataDisks: nodePoolDataDisks({ compute, packagePlan }),
        Unschedulable: 0,
        ExtraArgs: { Kubelet: [] }
      },
      EnableAutoscale: true,
      Name: name,
      Labels: labels,
      Taints: [],
      ContainerRuntime: this.env.OPL_TKE_CONTAINER_RUNTIME || "containerd",
      RuntimeVersion: this.env.OPL_TKE_RUNTIME_VERSION || "1.6.9",
      NodePoolOs: this.env.OPL_TKE_NODEPOOL_OS || "tlinux3.1x86_64",
      OsCustomizeType: this.env.OPL_TKE_OS_CUSTOMIZE_TYPE || "GENERAL",
      DeletionProtection: false
    };
  }

  async writeStorageVolumeManifest(input) {
    const stateDir = join(this.stateRootDir, compactId(input.storageId));
    await mkdir(stateDir, { recursive: true });
    const manifestPath = join(stateDir, "storage.pvc.k8s.json");
    await writeFile(manifestPath, `${JSON.stringify(this.storageVolumeManifest(input), null, 2)}\n`, { mode: 0o600 });
    return manifestPath;
  }

  async writeAttachmentManifest(input) {
    const stateDir = join(this.stateRootDir, compactId(input.attachment.id));
    await mkdir(stateDir, { recursive: true });
    const manifestPath = join(stateDir, "attachment.k8s.json");
    await writeFile(manifestPath, `${JSON.stringify(this.attachmentManifest(input), null, 2)}\n`, { mode: 0o600 });
    return manifestPath;
  }

  async writeWorkspaceEntrySecretManifest(input) {
    const stateDir = join(this.stateRootDir, compactId(input.workspaceId));
    await mkdir(stateDir, { recursive: true });
    const manifestPath = join(stateDir, "workspace-entry-secret.k8s.json");
    await writeFile(manifestPath, `${JSON.stringify(this.workspaceEntrySecretManifest(input), null, 2)}\n`, { mode: 0o600 });
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

  async removeWorkspaceRoutesForService({ serviceName }) {
    const ingress = await this.readSharedIngress();
    const nextIngress = sanitizeKubernetesApplyManifest(ingress);
    const rule = ensureIngressRule(nextIngress, this.workspaceHost());
    const currentPaths = Array.isArray(rule.http?.paths) ? rule.http.paths : [];
    const nextPaths = currentPaths.filter((candidate) => candidate.backend?.service?.name !== serviceName);
    if (nextPaths.length === currentPaths.length) return;
    rule.http.paths = sortWorkspacePaths(nextPaths);
    const manifestPath = await this.writeSharedIngressRouteManifest({
      workspaceId: serviceName,
      ingress: nextIngress
    });
    await this.runKubectl(["apply", "-f", manifestPath]);
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

  storageVolumeManifest({ name, storageId, accountId, storage, packagePlan }) {
    return {
      apiVersion: "v1",
      kind: "PersistentVolumeClaim",
      metadata: {
        name: `${name}-data`,
        labels: {
          "app.kubernetes.io/name": "opl-storage-volume",
          "app.kubernetes.io/instance": name,
          "oplcloud.cn/storage-id": storageId,
          "oplcloud.cn/account-id": accountId
        }
      },
      spec: this.workspacePvcSpec({
        packagePlan: {
          ...packagePlan,
          diskGb: storage.sizeGb || packagePlan.diskGb
        }
      })
    };
  }

  computeResourceManifest({ name, computeId, accountId, compute, packagePlan, storageClaimName = null }) {
    const labels = {
      "app.kubernetes.io/name": "opl-workspace-runtime",
      "app.kubernetes.io/instance": name,
      "oplcloud.cn/compute-id": computeId,
      "oplcloud.cn/account-id": accountId
    };
    const selector = { matchLabels: labels };
    const nodeSelector = compute.runtime?.nodeSelector || compute.nodeSelector || computeNodeSelector(computeId);
    const volumeMounts = storageClaimName
      ? [
        { name: "workspace-data", mountPath: "/data", subPath: "data" },
        { name: "workspace-data", mountPath: "/projects", subPath: "projects" }
      ]
      : undefined;
    const volumes = storageClaimName
      ? [{ name: "workspace-data", persistentVolumeClaim: { claimName: storageClaimName } }]
      : undefined;
    return {
      apiVersion: "v1",
      kind: "List",
      items: [
        this.workspaceEntrySecretManifest({
          computeName: name,
          workspaceId: compute.workspaceId || computeId,
          workspaceName: compute.name || computeId,
          ownerAccountId: accountId,
          packagePlan,
          token: compute.token || ""
        }),
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
                nodeSelector,
                initContainers: volumes
                  ? [this.codexBootstrapInitContainer({ secretName: `${name}-env` })]
                  : undefined,
                containers: [
                  {
                    name: "workspace",
                    image: this.env.OPL_WORKSPACE_IMAGE,
                    imagePullPolicy: "IfNotPresent",
                    ports: [{ name: "http", containerPort: Number(this.env.OPL_WORKSPACE_WEBUI_PORT || 3000) }],
                    envFrom: [{ secretRef: { name: `${name}-env` } }],
                    env: [
                      { name: "OPL_COMPUTE_ID", value: computeId },
                      { name: "OPL_OWNER_ACCOUNT_ID", value: accountId },
                      { name: "OPL_PACKAGE_ID", value: packagePlan.id },
                      { name: "OPL_ACCELERATOR", value: packagePlan.accelerator || "cpu" },
                      { name: "DATA_DIR", value: "/data" },
                      { name: "AIONUI_DATA_DIR", value: "/data" },
                      { name: "OPL_PROJECTS_DIR", value: "/projects" },
                      { name: "ALLOW_REMOTE", value: "true" },
                      { name: "OPL_WEBUI_AUTH_MODE", value: "none" },
                      { name: "AIONUI_WEBUI_AUTH_MODE", value: "none" },
                      { name: "HOME", value: "/data" },
                      { name: "OPL_WORKSPACE_ROOT", value: "/projects" },
                      { name: "CODEX_HOME", value: "/data/codex" }
                    ],
                    volumeMounts,
                    resources: workspaceContainerResources(packagePlan),
                    readinessProbe: {
                      httpGet: { path: "/", port: 3000 },
                      initialDelaySeconds: 10,
                      periodSeconds: 10
                    }
                  }
                ],
                volumes
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

  attachmentManifest({ attachment, compute, storage, computeName, storageClaimName }) {
    return this.computeResourceManifest({
      name: computeName,
      computeId: compute.id || attachment.computeId,
      accountId: compute.ownerAccountId || attachment.ownerAccountId || "unknown",
      compute,
      packagePlan: {
        id: compute.packageId || storage.packageId || "basic",
        server: compute.spec,
        cpu: compute.cpu,
        memoryGb: compute.memoryGb,
        accelerator: compute.accelerator || "cpu"
      },
      storageClaimName
    });
  }

  workspaceEntrySecretManifest({ computeName, workspaceId, workspaceName, ownerAccountId, packagePlan, token }) {
    return {
      apiVersion: "v1",
      kind: "Secret",
      metadata: {
        name: `${computeName}-env`,
        labels: {
          "app.kubernetes.io/name": "opl-workspace-entry",
          "app.kubernetes.io/instance": computeName,
          "oplcloud.cn/workspace-id": workspaceId
        }
      },
      type: "Opaque",
      data: {
        OPL_SHARE_TOKEN: b64(token),
        OPL_WORKSPACE_ID: b64(workspaceId),
        OPL_WORKSPACE_NAME: b64(workspaceName || ""),
        OPL_OWNER_ACCOUNT_ID: b64(ownerAccountId || ""),
        OPL_PACKAGE_ID: b64(packagePlan?.id || ""),
        ...this.workspaceCodexSecretData()
      }
    };
  }

  workspaceCodexSecretData() {
    return Object.fromEntries(WORKSPACE_CODEX_SECRET_KEYS.flatMap((key) => {
      const value = String(this.env[key] || "").trim();
      return value ? [[key, b64(value)]] : [];
    }));
  }

  codexBootstrapInitContainer({ secretName }) {
    return {
      name: "bootstrap-codex-config",
      image: this.env.OPL_WORKSPACE_IMAGE,
      imagePullPolicy: "IfNotPresent",
      envFrom: [{ secretRef: { name: secretName } }],
      env: [
        { name: "CODEX_HOME", value: "/data/codex" }
      ],
      command: ["node", "-e"],
      args: [codexBootstrapScript()],
      volumeMounts: [
        { name: "workspace-data", mountPath: "/data", subPath: "data" }
      ],
      securityContext: {
        allowPrivilegeEscalation: false,
        readOnlyRootFilesystem: false,
        capabilities: { drop: ["ALL"] }
      }
    };
  }

  workspacePvcSpec({ packagePlan }) {
    return {
      accessModes: ["ReadWriteOnce"],
      storageClassName: this.env.OPL_WORKSPACE_STORAGE_CLASS,
      resources: { requests: { storage: `${packagePlan.diskGb}Gi` } }
    };
  }
}

function codexBootstrapScript() {
  return `
const fs = require("node:fs");
const path = require("node:path");
const codexHome = process.env.CODEX_HOME || "/data/codex";
const configPath = path.join(codexHome, "config.toml");
const apiKey = String(process.env.OPL_CODEX_API_KEY || process.env.CODEX_API_KEY || process.env.OPENAI_API_KEY || "").trim();
const model = String(process.env.OPL_CODEX_MODEL || process.env.CODEX_MODEL || "gpt-5.5").trim();
const baseUrl = String(process.env.OPL_CODEX_BASE_URL || process.env.CODEX_BASE_URL || process.env.OPENAI_BASE_URL || "").trim();
if (!apiKey || !model || !baseUrl) process.exit(0);
const existing = fs.existsSync(configPath) ? fs.readFileSync(configPath, "utf8") : "";
if (/experimental_bearer_token\\s*=/.test(existing)) process.exit(0);
const providerId = String(process.env.OPL_CODEX_MODEL_PROVIDER || process.env.CODEX_MODEL_PROVIDER || "gflabtoken").trim();
const providerName = String(process.env.OPL_CODEX_PROVIDER_NAME || process.env.CODEX_PROVIDER_NAME || providerId).trim();
const reasoningEffort = String(process.env.OPL_CODEX_REASONING_EFFORT || process.env.CODEX_REASONING_EFFORT || "").trim();
const q = (value) => JSON.stringify(String(value));
const lines = [
  "model_provider = " + q(providerId),
  "model = " + q(model),
  ...(reasoningEffort ? ["model_reasoning_effort = " + q(reasoningEffort)] : []),
  "",
  "[model_providers." + providerId + "]",
  "name = " + q(providerName),
  "base_url = " + q(baseUrl),
  "experimental_bearer_token = " + q(apiKey),
  ""
];
fs.mkdirSync(codexHome, { recursive: true });
fs.writeFileSync(configPath, lines.join("\\n"), { mode: 0o600 });
fs.chmodSync(configPath, 0o600);
`.trim();
}

function parseJsonTemplate(value, name) {
  try {
    return JSON.parse(value);
  } catch (error) {
    throw new Error(`invalid_json_template:${name}:${error.message}`);
  }
}

function renderJsonTemplate(value, replacements) {
  return renderTemplate(parseJsonTemplate(value, "nodepool"), replacements);
}

function renderTemplate(value, replacements) {
  if (Array.isArray(value)) return value.map((item) => renderTemplate(item, replacements));
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, renderTemplate(item, replacements)]));
  }
  if (typeof value !== "string") return value;
  const replaced = value.replace(/\$\{([A-Z0-9_]+)\}/g, (_, key) => String(replacements[key] ?? ""));
  if (/^-?\d+(\.\d+)?$/.test(replaced)) return Number(replaced);
  if (replaced === "true") return true;
  if (replaced === "false") return false;
  return replaced;
}

function packageInstanceType(packagePlan = {}) {
  return packagePlan.instanceType || packagePlan.server || "SA5.LARGE8";
}

function computeDesiredNodes({ compute = {}, packagePlan = {} }) {
  const value = Number(compute.desiredNodes || packagePlan.desiredNodes || packagePlan.nodeCount || 1);
  return Number.isFinite(value) && value > 0 ? Math.ceil(value) : 1;
}

function nodePoolTemplateValues({ computeId, accountId, name, packagePlan, desiredNodes, compute = {} }) {
  return {
    COMPUTE_ID: computeId,
    ACCOUNT_ID: accountId,
    NODEPOOL_NAME: name,
    INSTANCE_TYPE: packageInstanceType(packagePlan),
    DESIRED_NODES: desiredNodes,
    MIN_NODES: Number(compute.minNodes || packagePlan.minNodes || 0),
    MAX_NODES: Number(compute.maxNodes || packagePlan.maxNodes || desiredNodes),
    SYSTEM_DISK_GB: Number(compute.systemDiskGb || packagePlan.systemDiskGb || 50),
    NODE_DATA_DISK_GB: Number(compute.nodeDataDiskGb || packagePlan.nodeDataDiskGb || packagePlan.diskGb || 50)
  };
}

function nodePoolLabels({ computeId, accountId }) {
  return [
    { Name: "oplcloud.cn/compute-id", Value: compactId(computeId) },
    { Name: "oplcloud.cn/account-id", Value: compactId(accountId) },
    { Name: "oplcloud.cn/runtime", Value: "one-person-lab-app" }
  ];
}

function nodePoolDataDisks({ compute = {}, packagePlan = {} }) {
  const size = Number(compute.nodeDataDiskGb || packagePlan.nodeDataDiskGb || 0);
  if (!Number.isFinite(size) || size <= 0) return [];
  return [
    {
      DiskType: compute.nodeDataDiskType || packagePlan.nodeDataDiskType || "CLOUD_PREMIUM",
      FileSystem: "ext4",
      DiskSize: size,
      AutoFormatAndMount: true,
      MountTarget: compute.nodeDataMountTarget || packagePlan.nodeDataMountTarget || "/data/opl-node",
      DiskPartition: "false"
    }
  ];
}

function nodePoolIdFromCompute(compute = {}) {
  if (compute.nodePoolId) return compute.nodePoolId;
  const ref = String(compute.providerResourceId || "");
  return ref.startsWith("nodepool/") ? resourceName(ref) : "";
}

function nodePoolReadyNodes(nodePool = {}) {
  const summary = nodePool.NodeCountSummary || {};
  return Number(summary.AutoscalingAdded?.Normal || 0) + Number(summary.ManuallyAdded?.Normal || 0);
}

function nodePoolReadyTimeoutMs(env) {
  const value = Number(env.OPL_TKE_NODEPOOL_READY_TIMEOUT_MS);
  return Number.isFinite(value) && value > 0 ? value : 0;
}

function nodePoolReadyPollMs(env) {
  const value = Number(env.OPL_TKE_NODEPOOL_READY_POLL_MS);
  return Number.isFinite(value) && value >= 0 ? value : 5000;
}

function resourceName(resourceId) {
  return String(resourceId || "").split("/").pop();
}

function computeNameFromAttachment(attachment) {
  const providerRef = String(attachment.providerAttachmentId || "").split(":")[0];
  return resourceName(providerRef || `deployment/${k8sName(attachment.computeId)}`);
}

function runtimeWorkloadName(compute = {}, fallbackId) {
  return compute.runtime?.workloadName ||
    compute.runtime?.serviceName ||
    resourceName(compute.server?.id || `deployment/${k8sName(fallbackId)}`);
}

function runtimeDeploymentName(workspace = {}) {
  return resourceName(
    workspace.docker?.id ||
    workspace.docker?.service ||
    workspace.runtime?.workloadName ||
    `deployment/${k8sName(workspace.computeId || workspace.id)}`
  );
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
