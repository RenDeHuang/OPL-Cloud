import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { TencentTkeProvider } from "../../packages/fabric/src/runtime-providers/tencent-tke.js";

const requiredEnv = {
  OPL_WORKSPACE_DOMAIN: "workspace.medopl.cn",
  OPL_WORKSPACE_IMAGE: "registry.example.com/opl/one-person-lab-app:2026-07-01",
  OPL_K8S_NAMESPACE: "opl-cloud",
  OPL_INGRESS_CLASS: "qcloud",
  OPL_IMAGE_PULL_SECRET_NAME: "tcr-pull-secret",
  OPL_WORKSPACE_STORAGE_CLASS: "cbs",
  OPL_TENCENT_PROVISIONER_BIN: "/usr/local/bin/opl-tencent-provisioner",
  OPL_WORKSPACE_NODE_SELECTOR_KEY: "medopl.cn/workload",
  OPL_WORKSPACE_NODE_SELECTOR_VALUE: "medopl",
  TENCENT_DEPLOY_KUBECONFIG_REF: "/tmp/kubeconfig"
};

function decodedSecretData(secret) {
  return Object.fromEntries(Object.entries(secret.data || {}).map(([key, value]) => [
    key,
    Buffer.from(value, "base64").toString("utf8")
  ]));
}

test("Tencent TKE provider reports readiness gaps before Kubernetes execution", async () => {
  const provider = new TencentTkeProvider({
    env: {},
    commandExists: () => false
  });

  const readiness = await provider.readiness();

  assert.deepEqual(readiness, {
    provider: "tencent-tke",
    ready: false,
    missingEnv: [
      "OPL_WORKSPACE_DOMAIN",
      "OPL_WORKSPACE_IMAGE",
      "OPL_K8S_NAMESPACE",
      "OPL_INGRESS_CLASS",
      "OPL_IMAGE_PULL_SECRET_NAME",
      "OPL_WORKSPACE_STORAGE_CLASS",
      "OPL_TENCENT_PROVISIONER_BIN",
      "TENCENT_DEPLOY_KUBECONFIG_REF"
    ],
    missingTools: ["kubectl"]
  });
});

test("Tencent TKE provider passes Codex provider settings through allocation and entry secrets", () => {
  const provider = new TencentTkeProvider({
    env: {
      ...requiredEnv,
      OPL_CODEX_MODEL: "gpt-5.5",
      OPL_CODEX_REASONING_EFFORT: "xhigh",
      OPL_CODEX_BASE_URL: "https://gflabtoken.cn/v1",
      OPL_CODEX_API_KEY: "secret-codex-key"
    },
    commandExists: () => true
  });
  const computeManifest = provider.computeAllocationManifest({
    name: "opl-compute-codex",
    computeAllocationId: "compute-codex",
    accountId: "pi-alpha",
    compute: { id: "compute-codex", name: "Codex node", token: "share_compute" },
    packagePlan: { id: "basic", accelerator: "cpu", cpu: 2, memoryGb: 4, server: "2c4g", diskGb: 10 }
  });
  const entrySecretManifest = provider.workspaceEntrySecretManifest({
    computeName: "opl-compute-codex",
    workspaceId: "ws-codex",
    ownerAccountId: "pi-alpha",
    workspaceName: "Codex Workspace",
    packagePlan: { id: "basic", accelerator: "cpu", cpu: 2, memoryGb: 4, server: "2c4g", diskGb: 10 },
    token: "share_workspace"
  });

  const computeSecret = decodedSecretData(computeManifest.items.find((item) => item.kind === "Secret"));
  const entrySecret = decodedSecretData(entrySecretManifest);
  assert.deepEqual({
    OPL_CODEX_MODEL: computeSecret.OPL_CODEX_MODEL,
    OPL_CODEX_REASONING_EFFORT: computeSecret.OPL_CODEX_REASONING_EFFORT,
    OPL_CODEX_BASE_URL: computeSecret.OPL_CODEX_BASE_URL,
    OPL_CODEX_API_KEY: computeSecret.OPL_CODEX_API_KEY
  }, {
    OPL_CODEX_MODEL: "gpt-5.5",
    OPL_CODEX_REASONING_EFFORT: "xhigh",
    OPL_CODEX_BASE_URL: "https://gflabtoken.cn/v1",
    OPL_CODEX_API_KEY: "secret-codex-key"
  });
  assert.equal(entrySecret.OPL_CODEX_API_KEY, "secret-codex-key");

  const computeEnv = computeManifest.items.find((item) => item.kind === "Deployment")
    .spec.template.spec.containers[0].env;
  assert.equal(JSON.stringify(computeEnv).includes("secret-codex-key"), false);
});

test("Tencent TKE provider bootstraps Codex config into attached storage before WebUI starts", () => {
  const provider = new TencentTkeProvider({
    env: {
      ...requiredEnv,
      OPL_CODEX_MODEL: "gpt-5.5",
      OPL_CODEX_REASONING_EFFORT: "xhigh",
      OPL_CODEX_BASE_URL: "https://gflabtoken.cn/v1",
      OPL_CODEX_API_KEY: "secret-codex-key"
    },
    commandExists: () => true
  });
  const manifest = provider.computeAllocationManifest({
    name: "opl-compute-codex",
    computeAllocationId: "compute-codex",
    accountId: "pi-alpha",
    compute: { id: "compute-codex", name: "Codex node", token: "share_compute" },
    packagePlan: { id: "basic", accelerator: "cpu", cpu: 2, memoryGb: 4, server: "2c4g", diskGb: 10 },
    storageClaimName: "opl-storage-codex-data"
  });
  const deployment = manifest.items.find((item) => item.kind === "Deployment");
  const initContainer = deployment.spec.template.spec.initContainers?.find((item) => item.name === "bootstrap-codex-config");

  assert.ok(initContainer, "allocation deployment must write Codex config before WebUI starts");
  assert.equal(initContainer.image, requiredEnv.OPL_WORKSPACE_IMAGE);
  assert.deepEqual(initContainer.envFrom, [{ secretRef: { name: "opl-compute-codex-env" } }]);
  assert.deepEqual(Object.fromEntries(initContainer.env.map((item) => [item.name, item.value])), {
    CODEX_HOME: "/data/codex"
  });
  assert.deepEqual(initContainer.volumeMounts, [
    { name: "workspace-data", mountPath: "/data", subPath: "data" }
  ]);
  assert.equal(JSON.stringify(initContainer).includes("secret-codex-key"), false);
  assert.match(initContainer.command.join(" "), /node/);
  assert.match(initContainer.args.join(" "), /experimental_bearer_token/);
});

test("Tencent TKE provider exposes split compute, storage, attachment, and Workspace entry operations", async () => {
  const stateRootDir = await mkdtemp(join(tmpdir(), "opl-cloud-tke-resource-state-"));
  const calls = [];
  const runner = async ({ command, args }) => {
    calls.push({ command, args });
    if (args.join(" ") === "--kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json") {
      return JSON.stringify(sharedIngressFixture());
    }
    return "";
  };
  const provider = new TencentTkeProvider({
    env: requiredEnv,
    runner,
    commandExists: () => true,
    provisionerClient: {
      async createComputeAllocation(input) {
        return {
          ok: true,
          operationId: "op-provisioner",
          poolId: input.pool.id,
          nodePoolId: "np-basic",
          instanceId: "ins-created",
          nodeName: "node-created",
          privateIp: "10.0.0.12",
          publicIp: "",
          status: "running",
          providerData: { scaleNodePoolRequestId: "req-scale", nodeName: "node-created", privateIp: "10.0.0.12" }
        };
      }
    },
    stateRootDir
  });
  const packagePlan = { id: "basic", accelerator: "cpu", cpu: 2, memoryGb: 4, gpu: 0, server: "2c4g", diskGb: 10 };

  try {
    const storage = await provider.createStorageVolume({
      storageId: "storage-tke001",
      accountId: "pi-alpha",
      storage: { id: "storage-tke001", ownerAccountId: "pi-alpha", sizeGb: 10 },
      packagePlan
    });
    const compute = await provider.createComputeAllocation({
      computeAllocationId: "compute-tke001",
      accountId: "pi-alpha",
      computeAllocation: { id: "compute-tke001", ownerAccountId: "pi-alpha", name: "CPU node" },
      packagePlan
    });
    const attachment = await provider.attachStorage({
      attachment: {
        id: "attach-tke001",
        ownerAccountId: "pi-alpha",
        computeAllocationId: "compute-tke001",
        storageId: "storage-tke001",
        mountPath: "/data"
      },
      compute: { id: "compute-tke001", ownerAccountId: "pi-alpha", name: "CPU node", ...compute },
      storage: { id: "storage-tke001", ownerAccountId: "pi-alpha", sizeGb: 10, ...storage }
    });
    const entry = await provider.createWorkspaceEntry({
      workspaceId: "ws-tke-resource",
      ownerAccountId: "pi-alpha",
      workspaceName: "Grant Lab",
      slug: "grant-lab-resource",
      token: "share_resource_secret",
      attachment: { id: "attach-tke001", mountPath: "/data", ...attachment },
      compute: { id: "compute-tke001", ownerAccountId: "pi-alpha", name: "CPU node", ...compute },
      storage: { id: "storage-tke001", ownerAccountId: "pi-alpha", sizeGb: 10, ...storage },
      packagePlan
    });

    const storageManifestPath = join(stateRootDir, "storage-tke001", "storage.pvc.k8s.json");
    const attachmentManifestPath = join(stateRootDir, "attach-tke001", "attachment.k8s.json");
    const entrySecretPath = join(stateRootDir, "ws-tke-resource", "workspace-entry-secret.k8s.json");
    assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${storageManifestPath}`,
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${attachmentManifestPath}`,
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${entrySecretPath}`
    ]);
    assert.equal(storage.providerResourceId, "pvc/opl-storage-tke001-data");
    assert.equal(compute.providerResourceId, "node/node-created");
    assert.equal(compute.nodePoolId, "np-basic");
    assert.equal(compute.instanceId, "ins-created");
    assert.equal(compute.nodeName, "node-created");
    assert.equal(attachment.providerAttachmentId, "deployment/opl-compute-tke001:pvc/opl-storage-tke001-data:/data");
    assert.equal(entry.url, "https://workspace.medopl.cn/w/ws-tke-resource/?token=share_resource_secret");

    const storageManifest = JSON.parse(await readFile(storageManifestPath, "utf8"));
    const attachmentManifest = JSON.parse(await readFile(attachmentManifestPath, "utf8"));
    const entrySecret = JSON.parse(await readFile(entrySecretPath, "utf8"));
    const attachmentDeployment = attachmentManifest.items.find((item) => item.kind === "Deployment");
    const attachmentContainer = attachmentDeployment.spec.template.spec.containers[0];

    assert.equal(storageManifest.kind, "PersistentVolumeClaim");
    assert.equal(storageManifest.metadata.name, "opl-storage-tke001-data");
    assert.equal(storageManifest.spec.resources.requests.storage, "10Gi");
    assert.equal(attachmentDeployment.metadata.name, "opl-compute-tke001");
    assert.equal(attachmentDeployment.spec.template.spec.nodeSelector["kubernetes.io/hostname"], "node-created");
    assert.equal(entrySecret.kind, "Secret");
    assert.equal(entrySecret.metadata.name, "opl-compute-tke001-env");
    assert.equal(Buffer.from(entrySecret.data.OPL_SHARE_TOKEN, "base64").toString("utf8"), "share_resource_secret");
    assert.equal(attachmentDeployment.spec.template.spec.volumes[0].persistentVolumeClaim.claimName, "opl-storage-tke001-data");
    assert.deepEqual(attachmentContainer.volumeMounts.map((mount) => `${mount.mountPath}:${mount.subPath}`), [
      "/data:data",
      "/projects:projects"
    ]);
    assert.deepEqual(Object.fromEntries(attachmentContainer.env.map((item) => [item.name, item.value])), {
      OPL_COMPUTE_ALLOCATION_ID: "compute-tke001",
      OPL_OWNER_ACCOUNT_ID: "pi-alpha",
      OPL_PACKAGE_ID: "basic",
      OPL_ACCELERATOR: "cpu",
      DATA_DIR: "/data",
      AIONUI_DATA_DIR: "/data",
      OPL_PROJECTS_DIR: "/projects",
      ALLOW_REMOTE: "true",
      OPL_WEBUI_AUTH_MODE: "none",
      AIONUI_WEBUI_AUTH_MODE: "none",
      HOME: "/data",
      OPL_WORKSPACE_ROOT: "/projects",
      CODEX_HOME: "/data/codex"
    });
  } finally {
    await rm(stateRootDir, { recursive: true, force: true });
  }
});

test("Tencent TKE provider opens account compute allocation through Go provisioner", async () => {
  const calls = [];
  const provisionerCalls = [];
  const provider = new TencentTkeProvider({
    env: {
      ...requiredEnv,
      OPL_TENCENT_PROVISIONER_BIN: "/usr/local/bin/opl-tencent-provisioner",
      OPL_TENCENT_PROVISIONER_DRY_RUN: "true"
    },
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      return "";
    },
    commandExists: () => true,
    provisionerClient: {
      async createComputeAllocation(input) {
        provisionerCalls.push(input);
        return {
          ok: true,
          operationId: "op-provisioner",
          poolId: input.pool.id,
          nodePoolId: "np-basic",
          instanceId: "ins-created",
          nodeName: "node-created",
          privateIp: "10.0.0.12",
          publicIp: "",
          status: "running",
          providerData: { scaleNodePoolRequestId: "req-scale", nodeName: "node-created", privateIp: "10.0.0.12" }
        };
      }
    }
  });

  const result = await provider.createComputeAllocation({
    computeAllocationId: "compute-tke001",
    accountId: "pi-alpha",
    userId: "usr-alpha",
    computeAllocation: { id: "compute-tke001", ownerAccountId: "pi-alpha", name: "CPU node" },
    packagePlan: { id: "basic", accelerator: "cpu", cpu: 2, memoryGb: 4, gpu: 0, server: "2c4g", diskGb: 10, instanceType: "SA5.LARGE4", nodePoolId: "np-basic" }
  });

  assert.equal(result.providerResourceId, "node/node-created");
  assert.equal(result.operationId, "op-provisioner");
  assert.equal(result.poolId, "pool-basic-2c4g");
  assert.equal(result.nodePoolId, "np-basic");
  assert.equal(result.instanceId, "ins-created");
  assert.equal(result.nodeName, "node-created");
  assert.equal(result.privateIp, "10.0.0.12");
  assert.deepEqual(result.runtime.nodeSelector, { "kubernetes.io/hostname": "node-created" });
  assert.equal(result.status, "running");
  assert.deepEqual(provisionerCalls.map((call) => call.allocation.id), ["compute-tke001"]);
  assert.equal(provisionerCalls[0].pool.instanceType, "SA5.LARGE4");
  assert.deepEqual(calls, []);
});

test("Tencent TKE provider destroys native node allocation through Go provisioner with node identity", async () => {
  const provisionerCalls = [];
  const provider = new TencentTkeProvider({
    env: requiredEnv,
    runner: async ({ command, args }) => {
      if (`${command} ${args.join(" ")}` === "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json") {
        return JSON.stringify(sharedIngressFixture());
      }
      return "";
    },
    commandExists: () => true,
    provisionerClient: {
      async destroyComputeAllocation(input) {
        provisionerCalls.push(input);
        return { ok: true, status: "destroyed" };
      }
    }
  });

  await provider.destroyComputeAllocation({
    computeAllocation: {
      id: "compute-tke001",
      ownerAccountId: "pi-alpha",
      poolId: "pool-basic-2c4g",
      nodePoolId: "np-basic",
      providerResourceId: "node/node-created",
      instanceId: "ins-created",
      nodeName: "node-created",
      runtime: { serviceName: "opl-compute-tke001" }
    }
  });

  assert.deepEqual(provisionerCalls, [{
    accountId: "pi-alpha",
    pool: {
      id: "pool-basic-2c4g",
      nodePoolId: "np-basic"
    },
    allocation: {
      id: "compute-tke001",
      instanceId: "ins-created",
      nodeName: "node-created"
    }
  }]);
});

test("Tencent TKE provider detaches and destroys split resources in Kubernetes", async () => {
  const stateRootDir = await mkdtemp(join(tmpdir(), "opl-cloud-tke-resource-cleanup-"));
  const calls = [];
  const runner = async ({ command, args }) => {
    calls.push({ command, args });
    if (args.join(" ") === "--kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json") {
      return JSON.stringify(sharedIngressFixture({
        workspacePaths: [{ path: "/w/ws-old", serviceName: "opl-compute-tke001" }]
      }));
    }
    return "";
  };
  const provider = new TencentTkeProvider({
    env: requiredEnv,
    runner,
    commandExists: () => true,
    stateRootDir
  });
  const compute = {
    id: "compute-tke001",
    providerResourceId: "deployment/opl-compute-tke001"
  };
  const storage = {
    id: "storage-tke001",
    providerResourceId: "pvc/opl-storage-tke001-data"
  };
  const attachment = {
    id: "attach-tke001",
    computeAllocationId: "compute-tke001",
    providerAttachmentId: "deployment/opl-compute-tke001:pvc/opl-storage-tke001-data:/data"
  };

  try {
    const detached = await provider.detachStorage({ attachment });
    const destroyedCompute = await provider.destroyComputeAllocation({ computeAllocation: compute });
    const destroyedStorage = await provider.destroyStorageVolume({ storage });
    const routePath = join(stateRootDir, "opl-compute-tke001", "shared-ingress-route.k8s.json");
    const detachPatch = JSON.stringify({
      spec: {
        template: {
          spec: {
            containers: [{ name: "workspace", volumeMounts: null }],
            volumes: null
          }
        }
      }
    });

    assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud patch deployment/opl-compute-tke001 --type=strategic -p ${detachPatch}`,
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json",
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${routePath}`,
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud delete deployment/opl-compute-tke001 service/opl-compute-tke001 secret/opl-compute-tke001-env --ignore-not-found=true",
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud delete pvc/opl-storage-tke001-data --ignore-not-found=true"
    ]);
    assert.equal(detached.status, "detached");
    assert.equal(destroyedCompute.status, "destroyed");
    assert.equal(destroyedStorage.status, "destroyed");
  } finally {
    await rm(stateRootDir, { recursive: true, force: true });
  }
});

test("Tencent TKE provider treats detach as idempotent when compute deployment is already gone", async () => {
  const calls = [];
  const provider = new TencentTkeProvider({
    env: requiredEnv,
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      if (args.includes("patch")) throw new Error('deployments.apps "opl-compute-gone" not found');
      return "";
    },
    commandExists: () => true
  });
  const detached = await provider.detachStorage({
    attachment: {
      id: "attach-gone",
      computeAllocationId: "compute-gone",
      providerAttachmentId: "deployment/opl-compute-gone:pvc/opl-storage-gone-data:/data"
    }
  });

  assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
    'kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud patch deployment/opl-compute-gone --type=strategic -p {"spec":{"template":{"spec":{"containers":[{"name":"workspace","volumeMounts":null}],"volumes":null}}}}'
  ]);
  assert.deepEqual(detached, {
    providerAttachmentId: "deployment/opl-compute-gone:pvc/opl-storage-gone-data:/data",
    status: "detached"
  });
});

test("Tencent TKE provider still destroys compute allocation when shared Ingress route cleanup fails", async () => {
  const calls = [];
  const provider = new TencentTkeProvider({
    env: requiredEnv,
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      const commandLine = `${command} ${args.join(" ")}`;
      if (commandLine === "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json") {
        return JSON.stringify(sharedIngressFixture({
          workspacePaths: [{ path: "/w/ws-tke505", serviceName: "opl-compute-tke505" }]
        }));
      }
      if (commandLine.endsWith("shared-ingress-route.k8s.json")) {
        throw new Error("shared_ingress_cleanup_failed");
      }
      return "";
    },
    commandExists: () => true,
    stateRootDir: ".runtime/test-tke"
  });
  const computeAllocation = {
    id: "compute-tke505",
    providerResourceId: "deployment/opl-compute-tke505"
  };

  const destroyed = await provider.destroyComputeAllocation({ computeAllocation });

  assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json",
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f .runtime/test-tke/opl-compute-tke505/shared-ingress-route.k8s.json",
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud delete deployment/opl-compute-tke505 service/opl-compute-tke505 secret/opl-compute-tke505-env --ignore-not-found=true"
  ]);
  assert.equal(destroyed.status, "destroyed");
  assert.equal(destroyed.billingStatus, "stopped");
  assert.equal(destroyed.routeCleanupStatus, "failed");
  assert.equal(destroyed.routeCleanupError, "shared_ingress_cleanup_failed");
});

test("Tencent TKE provider reports runtime status from Kubernetes resources without exposing the token", async () => {
  const calls = [];
  const provider = new TencentTkeProvider({
    env: requiredEnv,
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      return JSON.stringify(runtimeStatusFixture({
        name: "opl-compute-tke303",
        workspaceId: "ws-tke303",
        image: requiredEnv.OPL_WORKSPACE_IMAGE,
        ready: true
      }));
    },
    commandExists: () => true,
    stateRootDir: ".runtime/test-tke"
  });
  const workspace = {
    id: "ws-tke303",
    server: { id: "deployment/opl-compute-tke303" },
    docker: { id: "deployment/opl-compute-tke303", image: requiredEnv.OPL_WORKSPACE_IMAGE, service: "service/opl-compute-tke303" },
    disk: { id: "pvc/opl-compute-tke303-data", storageClass: "cbs" },
    access: { token: "share_runtime_status" },
    url: "https://workspace.medopl.cn/w/ws-tke303/?token=share_runtime_status"
  };

  const status = await provider.runtimeStatus({ workspace });

  assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get deployment/opl-compute-tke303 pvc/opl-compute-tke303-data service/opl-compute-tke303 ingress/opl-cloud endpoints/opl-compute-tke303 -o json"
  ]);
  assert.equal(JSON.stringify(status).includes("share_runtime_status"), false);
  assert.equal(status.provider, "tencent-tke");
  assert.equal(status.workspaceId, "ws-tke303");
  assert.equal(status.ready, true);
  assert.deepEqual(status.checks.map((check) => `${check.name}:${check.ok}`), [
    "deployment_ready:true",
    "workspace_image_pulled:true",
    "pvc_bound:true",
    "deployment_uses_retained_pvc:true",
    "service_targets_workspace:true",
    "service_endpoints_ready:true",
    "ingress_routes_workspace_gateway:true"
  ]);
  assert.equal(status.resources.deployment.image, requiredEnv.OPL_WORKSPACE_IMAGE);
  assert.equal(status.resources.pvc.name, "opl-compute-tke303-data");
  assert.equal(status.resources.pvc.phase, "Bound");
  assert.equal(status.resources.service.name, "opl-compute-tke303");
  assert.equal(status.resources.ingress.name, "opl-cloud");
  assert.equal(status.resources.ingress.path, "/");
  assert.equal(status.resources.endpoints.readyAddresses, 1);
});

test("Tencent TKE provider checks runtime deployment from dedicated node-backed service", async () => {
  const calls = [];
  const provider = new TencentTkeProvider({
    env: requiredEnv,
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      return JSON.stringify(runtimeStatusFixture({
        name: "opl-compute-tke404",
        workspaceId: "ws-tke404",
        image: requiredEnv.OPL_WORKSPACE_IMAGE,
        ready: true
      }));
    },
    commandExists: () => true,
    stateRootDir: ".runtime/test-tke"
  });
  const workspace = {
    id: "ws-tke404",
    server: { id: "node/node-created" },
    docker: { id: "runtime-ws-tke404", image: requiredEnv.OPL_WORKSPACE_IMAGE, service: "service/opl-compute-tke404" },
    disk: { id: "pvc/opl-compute-tke404-data", storageClass: "cbs" },
    access: { token: "share_runtime_status" },
    url: "https://workspace.medopl.cn/w/ws-tke404/?token=share_runtime_status"
  };

  const status = await provider.runtimeStatus({ workspace });

  assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get deployment/opl-compute-tke404 pvc/opl-compute-tke404-data service/opl-compute-tke404 ingress/opl-cloud endpoints/opl-compute-tke404 -o json"
  ]);
  assert.equal(status.ready, true);
});

function sharedIngressFixture({ workspacePaths = [] } = {}) {
  return {
    apiVersion: "networking.k8s.io/v1",
    kind: "Ingress",
    metadata: {
      name: "opl-cloud",
      namespace: "opl-cloud",
      uid: "cluster-generated-uid",
      resourceVersion: "12345",
      generation: 4,
      managedFields: [{ manager: "tke" }],
      creationTimestamp: "2026-07-01T10:00:00Z",
      annotations: {
        "ingress.cloud.tencent.com/direct-access": "true"
      }
    },
    spec: {
      ingressClassName: "qcloud",
      rules: [
        {
          host: "cloud.medopl.cn",
          http: {
            paths: [
              {
                path: "/",
                pathType: "Prefix",
                backend: { service: { name: "opl-cloud-control-plane", port: { number: 8787 } } }
              }
            ]
          }
        },
        {
          host: "workspace.medopl.cn",
          http: {
            paths: [
              ...workspacePaths.map((item) => ({
                path: item.path,
                pathType: "Prefix",
                backend: { service: { name: item.serviceName, port: { number: 3000 } } }
              })),
              {
                path: "/",
                pathType: "Prefix",
                backend: { service: { name: "opl-cloud-control-plane", port: { number: 8787 } } }
              }
            ]
          }
        }
      ]
    },
    status: {
      loadBalancer: {
        ingress: [{ hostname: "lb-example.clb.tencentcloud.com" }]
      }
    }
  };
}

function runtimeStatusFixture({ name, workspaceId, image, ready }) {
  return {
    apiVersion: "v1",
    kind: "List",
    items: [
      {
        apiVersion: "apps/v1",
        kind: "Deployment",
        metadata: {
          name,
          labels: {
            "app.kubernetes.io/name": "opl-compute-allocation",
            "app.kubernetes.io/instance": name,
            "oplcloud.cn/workspace-id": workspaceId
          }
        },
        spec: {
          template: {
            metadata: {
              labels: {
                "app.kubernetes.io/name": "opl-compute-allocation",
                "app.kubernetes.io/instance": name,
                "oplcloud.cn/workspace-id": workspaceId
              }
            },
            spec: {
              containers: [{ name: "workspace", image }],
              volumes: [{ name: "workspace-data", persistentVolumeClaim: { claimName: `${name}-data` } }]
            }
          }
        },
        status: ready ? { readyReplicas: 1, availableReplicas: 1 } : { readyReplicas: 0, availableReplicas: 0 }
      },
      {
        apiVersion: "v1",
        kind: "PersistentVolumeClaim",
        metadata: { name: `${name}-data` },
        spec: { storageClassName: "cbs" },
        status: { phase: "Bound" }
      },
      {
        apiVersion: "v1",
        kind: "Service",
        metadata: { name },
        spec: {
          selector: {
            "app.kubernetes.io/name": "opl-compute-allocation",
            "app.kubernetes.io/instance": name,
            "oplcloud.cn/workspace-id": workspaceId
          },
          ports: [{ name: "http", port: 3000, targetPort: "http" }]
        }
      },
      {
        apiVersion: "networking.k8s.io/v1",
        kind: "Ingress",
        metadata: { name: "opl-cloud" },
        spec: {
          rules: [
            {
              host: "workspace.medopl.cn",
              http: {
                paths: [
                  {
                    path: "/",
                    backend: { service: { name: "opl-cloud-control-plane", port: { number: 8787 } } }
                  }
                ]
              }
            }
          ]
        }
      },
      {
        apiVersion: "v1",
        kind: "Endpoints",
        metadata: { name },
        subsets: ready ? [{ addresses: [{ ip: "10.0.0.8" }], ports: [{ name: "http", port: 3000 }] }] : []
      }
    ]
  };
}
