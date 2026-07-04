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
  TENCENT_DEPLOY_KUBECONFIG_REF: "/tmp/kubeconfig",
  TENCENT_DEPLOY_CLUSTER_ID: "cls-opl",
  TENCENT_TKE_REGION: "na-siliconvalley",
  TENCENT_MUTATION_SECRET_ID: "secret-id",
  TENCENT_MUTATION_SECRET_KEY: "secret-key",
  OPL_TKE_NODEPOOL_AUTOSCALING_GROUP_PARA_JSON: JSON.stringify({
    MinSize: 0,
    MaxSize: 1,
    DesiredCapacity: 1,
    VpcId: "vpc-opl",
    SubnetIds: ["subnet-opl"]
  }),
  OPL_TKE_NODEPOOL_LAUNCH_CONFIGURE_PARA_JSON: JSON.stringify({
    InstanceType: "${INSTANCE_TYPE}",
    SystemDisk: { DiskType: "CLOUD_PREMIUM", DiskSize: "${SYSTEM_DISK_GB}" },
    DataDisks: [{ DiskType: "CLOUD_PREMIUM", DiskSize: "${NODE_DATA_DISK_GB}" }]
  })
};

function decodedSecretData(secret) {
  return Object.fromEntries(Object.entries(secret.data || {}).map(([key, value]) => [
    key,
    Buffer.from(value, "base64").toString("utf8")
  ]));
}

function sharedIngressFixture({ workspacePaths = [] } = {}) {
  return {
    apiVersion: "networking.k8s.io/v1",
    kind: "Ingress",
    metadata: {
      name: "opl-cloud",
      namespace: "opl-cloud",
      resourceVersion: "rv-1",
      uid: "uid-1",
      managedFields: []
    },
    spec: {
      rules: [
        {
          host: "workspace.medopl.cn",
          http: {
            paths: [
              ...workspacePaths.map(({ path, serviceName }) => ({
                path,
                pathType: "Prefix",
                backend: { service: { name: serviceName, port: { number: 3000 } } }
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
    status: { loadBalancer: {} }
  };
}

function runtimeStatusFixture({ name, image, ready = true }) {
  return {
    apiVersion: "v1",
    kind: "List",
    items: [
      {
        apiVersion: "apps/v1",
        kind: "Deployment",
        metadata: { name },
        spec: {
          template: {
            metadata: {
              labels: {
                "app.kubernetes.io/name": "opl-workspace-runtime",
                "app.kubernetes.io/instance": name
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
            "app.kubernetes.io/name": "opl-workspace-runtime",
            "app.kubernetes.io/instance": name
          },
          ports: [{ name: "http", port: 3000, targetPort: "http" }]
        }
      },
      sharedIngressFixture(),
      {
        apiVersion: "v1",
        kind: "Endpoints",
        metadata: { name },
        subsets: ready ? [{ addresses: [{ ip: "10.0.0.8" }], ports: [{ port: 3000 }] }] : []
      }
    ]
  };
}

test("Tencent TKE provider reports readiness gaps before provider execution", async () => {
  const provider = new TencentTkeProvider({
    env: {},
    commandExists: () => false
  });

  const readiness = await provider.readiness();

  assert.equal(readiness.ready, false);
  assert.deepEqual(readiness.missingTools, ["kubectl", "tccli"]);
  assert.deepEqual(readiness.missingEnv, [
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
  ]);
});

test("Tencent TKE runtime manifest bootstraps Codex settings without exposing secrets in env", () => {
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

  const manifest = provider.computeResourceManifest({
    name: "opl-compute-codex",
    computeId: "compute-codex",
    accountId: "pi-alpha",
    compute: { id: "compute-codex", name: "Codex node", token: "share_compute" },
    packagePlan: { id: "basic", accelerator: "cpu", cpu: 2, memoryGb: 4, server: "2c4g", diskGb: 10 },
    storageClaimName: "opl-storage-codex-data"
  });

  const secret = decodedSecretData(manifest.items.find((item) => item.kind === "Secret"));
  const deployment = manifest.items.find((item) => item.kind === "Deployment");
  const initContainer = deployment.spec.template.spec.initContainers.find((item) => item.name === "bootstrap-codex-config");
  const runtimeContainer = deployment.spec.template.spec.containers[0];

  assert.equal(secret.OPL_CODEX_MODEL, "gpt-5.5");
  assert.equal(secret.OPL_CODEX_API_KEY, "secret-codex-key");
  assert.equal(JSON.stringify(runtimeContainer.env).includes("secret-codex-key"), false);
  assert.equal(initContainer.image, requiredEnv.OPL_WORKSPACE_IMAGE);
  assert.match(initContainer.args.join(" "), /experimental_bearer_token/);
});

test("Tencent TKE provider provisions node pool compute, PVC storage, runtime attachment, URL entry, and explicit cleanup", async () => {
  const stateRootDir = await mkdtemp(join(tmpdir(), "opl-cloud-tke-resource-state-"));
  const calls = [];
  const runner = async ({ command, args }) => {
    calls.push({ command, args });
    const commandLine = `${command} ${args.join(" ")}`;
    if (command === "tccli" && args.includes("CreateClusterNodePool")) {
      return JSON.stringify({ Response: { NodePoolId: "np-compute-tke001", RequestId: "req-create-nodepool" } });
    }
    if (command === "tccli" && args.includes("DescribeClusterNodePoolDetail")) {
      return JSON.stringify({
        Response: {
          NodePool: {
            NodePoolId: "np-compute-tke001",
            Name: "opl-compute-tke001",
            LifeState: "normal",
            DesiredNodesNum: 1,
            NodeCountSummary: {
              AutoscalingAdded: { Normal: 1, Total: 1 },
              ManuallyAdded: { Normal: 0, Total: 0 }
            }
          },
          RequestId: "req-nodepool-detail"
        }
      });
    }
    if (commandLine === "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json") {
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
  const packagePlan = { id: "basic", accelerator: "cpu", cpu: 2, memoryGb: 4, gpu: 0, server: "SA5.LARGE8", diskGb: 10 };

  try {
    const storage = await provider.createStorageVolume({
      storageId: "storage-tke001",
      accountId: "pi-alpha",
      storage: { id: "storage-tke001", ownerAccountId: "pi-alpha", sizeGb: 10 },
      packagePlan
    });
    const compute = await provider.createComputeResource({
      computeId: "compute-tke001",
      accountId: "pi-alpha",
      compute: { id: "compute-tke001", ownerAccountId: "pi-alpha", name: "CPU node" },
      packagePlan
    });
    const attachment = await provider.attachStorage({
      attachment: {
        id: "attach-tke001",
        ownerAccountId: "pi-alpha",
        computeId: "compute-tke001",
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
    const detached = await provider.detachStorage({ attachment: { ...attachment, computeId: "compute-tke001" } });
    const destroyedCompute = await provider.destroyComputeResource({
      compute: { id: "compute-tke001", nodePoolId: compute.nodePoolId, runtime: compute.runtime }
    });
    const destroyedStorage = await provider.destroyStorageVolume({ storage: { id: "storage-tke001", ...storage } });

    const storageManifestPath = join(stateRootDir, "storage-tke001", "storage.pvc.k8s.json");
    const attachmentManifestPath = join(stateRootDir, "attach-tke001", "attachment.k8s.json");
    const entrySecretPath = join(stateRootDir, "ws-tke-resource", "workspace-entry-secret.k8s.json");
    const commandLines = calls.map((call) => `${call.command} ${call.args.join(" ")}`);

    assert.equal(commandLines.includes(`kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${storageManifestPath}`), true);
    assert.equal(commandLines.some((line) => line.includes("tccli tke CreateClusterNodePool")), true);
    assert.equal(commandLines.some((line) => line.includes("tccli tke DescribeClusterNodePoolDetail")), true);
    assert.equal(commandLines.some((line) => line.includes("tccli tke DeleteClusterNodePool")), true);
    assert.equal(commandLines.some((line) => line.includes("compute.k8s.json")), false);
    assert.equal(commandLines.includes(`kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${attachmentManifestPath}`), true);
    assert.equal(commandLines.includes(`kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${entrySecretPath}`), true);
    assert.equal(commandLines.includes("kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud delete deployment/opl-compute-tke001 service/opl-compute-tke001 secret/opl-compute-tke001-env --ignore-not-found=true"), true);
    assert.equal(commandLines.includes("kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud delete pvc/opl-storage-tke001-data --ignore-not-found=true"), true);
    assert.equal(storage.providerResourceId, "pvc/opl-storage-tke001-data");
    assert.equal(compute.providerResourceId, "nodepool/np-compute-tke001");
    assert.equal(compute.nodePoolId, "np-compute-tke001");
    assert.deepEqual(compute.runtime.nodeSelector, { "oplcloud.cn/compute-id": "compute-tke001" });
    assert.equal(attachment.providerAttachmentId, "deployment/opl-compute-tke001:pvc/opl-storage-tke001-data:/data");
    assert.equal(entry.url, "https://workspace.medopl.cn/w/ws-tke-resource/?token=share_resource_secret");
    assert.equal(detached.status, "detached");
    assert.equal(destroyedCompute.providerResourceId, "nodepool/np-compute-tke001");
    assert.equal(destroyedStorage.providerResourceId, "pvc/opl-storage-tke001-data");

    const attachmentManifest = JSON.parse(await readFile(attachmentManifestPath, "utf8"));
    const attachmentDeployment = attachmentManifest.items.find((item) => item.kind === "Deployment");
    const entrySecret = JSON.parse(await readFile(entrySecretPath, "utf8"));
    assert.deepEqual(attachmentDeployment.spec.template.spec.nodeSelector, { "oplcloud.cn/compute-id": "compute-tke001" });
    assert.equal(Buffer.from(entrySecret.data.OPL_SHARE_TOKEN, "base64").toString("utf8"), "share_resource_secret");
  } finally {
    await rm(stateRootDir, { recursive: true, force: true });
  }
});

test("Tencent TKE provider waits for node pool capacity before returning running compute", async () => {
  const calls = [];
  let describeCount = 0;
  const provider = new TencentTkeProvider({
    env: {
      ...requiredEnv,
      OPL_TKE_NODEPOOL_READY_TIMEOUT_MS: "50",
      OPL_TKE_NODEPOOL_READY_POLL_MS: "0"
    },
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      if (command === "tccli" && args.includes("CreateClusterNodePool")) {
        return JSON.stringify({ Response: { NodePoolId: "np-compute-wait", RequestId: "req-create-nodepool" } });
      }
      if (command === "tccli" && args.includes("DescribeClusterNodePoolDetail")) {
        describeCount += 1;
        return JSON.stringify({
          Response: {
            NodePool: {
              NodePoolId: "np-compute-wait",
              Name: "opl-compute-wait",
              LifeState: describeCount > 1 ? "normal" : "creating",
              DesiredNodesNum: 1,
              NodeCountSummary: {
                AutoscalingAdded: { Normal: describeCount > 1 ? 1 : 0, Total: 1 },
                ManuallyAdded: { Normal: 0, Total: 0 }
              }
            }
          }
        });
      }
      return "";
    },
    commandExists: () => true
  });

  const compute = await provider.createComputeResource({
    computeId: "compute-wait",
    accountId: "pi-alpha",
    compute: { id: "compute-wait", ownerAccountId: "pi-alpha", name: "CPU node" },
    packagePlan: { id: "basic", accelerator: "cpu", cpu: 2, memoryGb: 4, gpu: 0, server: "SA5.LARGE8", diskGb: 10 }
  });

  assert.equal(compute.status, "running");
  assert.equal(compute.readyNodes, 1);
  assert.equal(
    calls.filter((call) => call.command === "tccli" && call.args.includes("DescribeClusterNodePoolDetail")).length,
    2
  );
});

test("Tencent TKE runtime status inspects the runtime deployment instead of the node pool handle", async () => {
  const calls = [];
  const provider = new TencentTkeProvider({
    env: requiredEnv,
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      return JSON.stringify(runtimeStatusFixture({
        name: "opl-compute-tke303",
        image: requiredEnv.OPL_WORKSPACE_IMAGE,
        ready: true
      }));
    },
    commandExists: () => true
  });

  const status = await provider.runtimeStatus({
    workspace: {
      id: "ws-tke303",
      computeId: "compute-tke303",
      server: { id: "nodepool/np-compute-tke303", status: "running", billingStatus: "active", spec: "SA5.LARGE8" },
      docker: { id: "deployment/opl-compute-tke303", image: requiredEnv.OPL_WORKSPACE_IMAGE, service: "service/opl-compute-tke303" },
      disk: { id: "pvc/opl-compute-tke303-data", status: "attached_retained", billingStatus: "active", sizeGb: 10 }
    }
  });

  assert.equal(status.ready, true);
  assert.equal(status.resources.deployment.name, "opl-compute-tke303");
  assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get deployment/opl-compute-tke303 pvc/opl-compute-tke303-data service/opl-compute-tke303 ingress/opl-cloud endpoints/opl-compute-tke303 -o json"
  ]);
});
