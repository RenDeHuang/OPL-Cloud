import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { TencentTkeProvider } from "../../services/api/src/runtime-providers/tencent-tke.js";

const requiredEnv = {
  OPL_WORKSPACE_DOMAIN: "workspace.medopl.cn",
  OPL_WORKSPACE_IMAGE: "registry.example.com/opl/one-person-lab-app:2026-07-01",
  OPL_K8S_NAMESPACE: "opl-cloud",
  OPL_INGRESS_CLASS: "qcloud",
  OPL_IMAGE_PULL_SECRET_NAME: "tcr-pull-secret",
  OPL_WORKSPACE_STORAGE_CLASS: "cbs",
  OPL_WORKSPACE_STORAGE_SIZE_GB: "20",
  OPL_WORKSPACE_NODE_SELECTOR_KEY: "medopl.cn/workload",
  OPL_WORKSPACE_NODE_SELECTOR_VALUE: "medopl",
  TENCENT_DEPLOY_KUBECONFIG_REF: "/tmp/kubeconfig"
};

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
      "TENCENT_DEPLOY_KUBECONFIG_REF"
    ],
    missingTools: ["kubectl"]
  });
});

test("Tencent TKE provider applies runtime resources and registers the Workspace path on the shared Ingress", async () => {
  const stateRootDir = await mkdtemp(join(tmpdir(), "opl-cloud-tke-state-"));
  const calls = [];
  const runner = async ({ command, args, cwd, env }) => {
    calls.push({ command, args, cwd, env });
    if (args.join(" ") === "--kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json") {
      return JSON.stringify(sharedIngressFixture());
    }
    if (args.join(" ") === "--kubeconfig /tmp/kubeconfig --namespace opl-cloud get deployment/opl-ws-tke001 pvc/opl-ws-tke001-data service/opl-ws-tke001 ingress/opl-cloud endpoints/opl-ws-tke001 -o json") {
      return JSON.stringify(runtimeStatusFixture({
        name: "opl-ws-tke001",
        workspaceId: "ws-tke001",
        image: requiredEnv.OPL_WORKSPACE_IMAGE,
        ready: true
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

  try {
    const runtime = await provider.createWorkspaceRuntime({
      workspaceId: "ws-tke001",
      ownerAccountId: "pi-alpha",
      workspaceName: "Grant Lab",
      packagePlan: { id: "basic", accelerator: "cpu", cpu: 2, memoryGb: 4, gpu: 0, server: "2c4g", diskGb: 10 },
      token: "share_tke_secret"
    });

    assert.equal(runtime.provider, "tencent-tke");
    assert.equal(runtime.server.id, "deployment/opl-ws-tke001");
    assert.equal(runtime.server.status, "running");
    assert.equal(runtime.server.spec, "2c4g");
    assert.equal(runtime.docker.id, "deployment/opl-ws-tke001");
    assert.equal(runtime.docker.image, requiredEnv.OPL_WORKSPACE_IMAGE);
    assert.equal(runtime.docker.status, "running");
    assert.equal(runtime.disk.id, "pvc/opl-ws-tke001-data");
    assert.equal(runtime.disk.sizeGb, 10);
    assert.equal(runtime.disk.mountPath, "/data");
    assert.equal(runtime.url, "https://workspace.medopl.cn/w/ws-tke001?token=share_tke_secret");
    assert.equal(runtime.slug, "grant-lab-tke001");

    const manifestPath = join(stateRootDir, "ws-tke001", "workspace.k8s.json");
    const routePath = join(stateRootDir, "ws-tke001", "shared-ingress-route.k8s.json");
    const commandLines = calls.map((call) => `${call.command} ${call.args.join(" ")}`);
    assert.equal(commandLines.join("\n").includes("share_tke_secret"), false);
    assert.deepEqual(commandLines, [
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${manifestPath}`,
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json",
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${routePath}`,
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get deployment/opl-ws-tke001 pvc/opl-ws-tke001-data service/opl-ws-tke001 ingress/opl-cloud endpoints/opl-ws-tke001 -o json"
    ]);

    const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
    assert.equal(manifest.kind, "List");
    assert.deepEqual(manifest.items.map((item) => item.kind), [
      "Secret",
      "PersistentVolumeClaim",
      "Deployment",
      "Service"
    ]);
    const deployment = manifest.items.find((item) => item.kind === "Deployment");
    const pvc = manifest.items.find((item) => item.kind === "PersistentVolumeClaim");
    const service = manifest.items.find((item) => item.kind === "Service");
    const container = deployment.spec.template.spec.containers[0];
    assert.equal(container.image, requiredEnv.OPL_WORKSPACE_IMAGE);
    assert.deepEqual(container.resources, {
      requests: { cpu: "2", memory: "4Gi" },
      limits: { cpu: "2", memory: "4Gi" }
    });
    assert.deepEqual(deployment.spec.template.spec.imagePullSecrets, [{ name: "tcr-pull-secret" }]);
    assert.deepEqual(deployment.spec.template.spec.nodeSelector, { "medopl.cn/workload": "medopl" });
    assert.equal(container.ports[0].containerPort, 3000);
    assert.equal(pvc.metadata.name, "opl-ws-tke001-data");
    assert.equal(pvc.spec.storageClassName, "cbs");
    assert.equal(deployment.spec.template.spec.volumes[0].persistentVolumeClaim.claimName, pvc.metadata.name);
    assert.deepEqual(container.volumeMounts.map((mount) => `${mount.mountPath}:${mount.subPath}`), [
      "/data:data",
      "/projects:projects"
    ]);
    assert.deepEqual(service.spec.selector, deployment.spec.template.metadata.labels);
    assert.equal(service.spec.ports[0].targetPort, "http");
    const ingress = JSON.parse(await readFile(routePath, "utf8"));
    assert.equal(ingress.metadata.name, "opl-cloud");
    assert.equal(ingress.metadata.uid, undefined);
    assert.equal(ingress.metadata.resourceVersion, undefined);
    assert.equal(ingress.metadata.managedFields, undefined);
    assert.equal(ingress.status, undefined);
    assert.equal(ingress.metadata.annotations["ingress.cloud.tencent.com/direct-access"], "true");
    assert.equal(ingress.spec.ingressClassName, "qcloud");
    const workspaceRule = ingress.spec.rules.find((rule) => rule.host === "workspace.medopl.cn");
    assert.deepEqual(workspaceRule.http.paths.map((path) => `${path.path}->${path.backend.service.name}:${path.backend.service.port.number}`), [
      "/w/ws-tke001->opl-ws-tke001:3000",
      "/->opl-cloud-control-plane:8787"
    ]);
  } finally {
    await rm(stateRootDir, { recursive: true, force: true });
  }
});

test("Tencent TKE provider scales compute lifecycle without deleting retained storage", async () => {
  const calls = [];
  const provider = new TencentTkeProvider({
    env: requiredEnv,
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      if (args.join(" ") === "--kubeconfig /tmp/kubeconfig --namespace opl-cloud get deployment/opl-ws-tke101 pvc/opl-ws-tke101-data service/opl-ws-tke101 ingress/opl-cloud endpoints/opl-ws-tke101 -o json") {
        return JSON.stringify(runtimeStatusFixture({
          name: "opl-ws-tke101",
          workspaceId: "ws-tke101",
          image: requiredEnv.OPL_WORKSPACE_IMAGE,
          ready: true
        }));
      }
      if (args.join(" ") === "--kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json") {
        return JSON.stringify(sharedIngressFixture({
          workspacePaths: [{ path: "/w/ws-tke101", serviceName: "opl-ws-tke101" }]
        }));
      }
      return "";
    },
    commandExists: () => true,
    stateRootDir: ".runtime/test-tke"
  });
  const workspace = {
    id: "ws-tke101",
    name: "Lifecycle Lab",
    packageId: "basic",
    slug: "lifecycle-lab-tke101",
    access: { token: "share_lifecycle" },
    server: { id: "deployment/opl-ws-tke101", status: "running", billingStatus: "active", spec: "2c4g" },
    docker: { image: requiredEnv.OPL_WORKSPACE_IMAGE },
    disk: { id: "pvc/opl-ws-tke101-data", status: "attached_retained", billingStatus: "active", sizeGb: 10 }
  };

  const stopped = await provider.stopServer({ workspace });
  const restarted = await provider.restartServer({ workspace: { ...workspace, server: stopped } });
  const destroyed = await provider.destroyServer({ workspace });
  const disk = await provider.destroyDisk({ workspace: { ...workspace, server: destroyed } });

  assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud scale deployment/opl-ws-tke101 --replicas=0",
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud scale deployment/opl-ws-tke101 --replicas=1",
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get deployment/opl-ws-tke101 pvc/opl-ws-tke101-data service/opl-ws-tke101 ingress/opl-cloud endpoints/opl-ws-tke101 -o json",
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json",
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f .runtime/test-tke/ws-tke101/shared-ingress-route.k8s.json",
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud delete deployment/opl-ws-tke101 service/opl-ws-tke101 secret/opl-ws-tke101-env --ignore-not-found=true",
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud delete pvc/opl-ws-tke101-data --ignore-not-found=true"
  ]);
  assert.equal(stopped.status, "stopped");
  assert.equal(stopped.billingStatus, "stopped");
  assert.equal(restarted.status, "running");
  assert.equal(restarted.billingStatus, "active");
  assert.equal(destroyed.status, "destroyed");
  assert.equal(destroyed.billingStatus, "stopped");
  assert.equal(disk.status, "destroyed");
  assert.equal(disk.billingStatus, "stopped");
});

test("Tencent TKE provider recreates compute from retained PVC after server destroy", async () => {
  const stateRootDir = await mkdtemp(join(tmpdir(), "opl-cloud-tke-state-"));
  const calls = [];
  const provider = new TencentTkeProvider({
    env: requiredEnv,
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      if (args.join(" ") === "--kubeconfig /tmp/kubeconfig --namespace opl-cloud get deployment/opl-ws-tke202 pvc/opl-ws-tke202-data service/opl-ws-tke202 ingress/opl-cloud endpoints/opl-ws-tke202 -o json") {
        return JSON.stringify(runtimeStatusFixture({
          name: "opl-ws-tke202",
          workspaceId: "ws-tke202",
          image: requiredEnv.OPL_WORKSPACE_IMAGE,
          ready: true
        }));
      }
      if (args.join(" ") === "--kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json") {
        return JSON.stringify(sharedIngressFixture());
      }
      return "";
    },
    commandExists: () => true,
    stateRootDir
  });
  const workspace = {
    id: "ws-tke202",
    ownerAccountId: "pi-alpha",
    name: "Recreate Lab",
    packageId: "basic",
    slug: "recreate-lab-tke202",
    access: { token: "share_recreate" },
    server: { id: "deployment/opl-ws-tke202", status: "destroyed", billingStatus: "stopped", spec: "2c4g" },
    docker: { image: requiredEnv.OPL_WORKSPACE_IMAGE, status: "destroyed" },
    disk: { id: "pvc/opl-ws-tke202-data", status: "detached_retained", billingStatus: "active", sizeGb: 10 }
  };

  try {
    const server = await provider.recreateServer({ workspace });
    const manifestPath = join(stateRootDir, "ws-tke202", "workspace.k8s.json");
    const routePath = join(stateRootDir, "ws-tke202", "shared-ingress-route.k8s.json");

    assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${manifestPath}`,
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json",
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${routePath}`,
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get deployment/opl-ws-tke202 pvc/opl-ws-tke202-data service/opl-ws-tke202 ingress/opl-cloud endpoints/opl-ws-tke202 -o json"
    ]);
    assert.equal(server.id, "deployment/opl-ws-tke202");
    assert.equal(server.status, "running");
    const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
    const pvc = manifest.items.find((item) => item.kind === "PersistentVolumeClaim");
    const deployment = manifest.items.find((item) => item.kind === "Deployment");
    assert.equal(pvc.metadata.name, "opl-ws-tke202-data");
    assert.equal(pvc.spec.resources.requests.storage, "10Gi");
    assert.equal(deployment.spec.template.spec.volumes[0].persistentVolumeClaim.claimName, pvc.metadata.name);
  } finally {
    await rm(stateRootDir, { recursive: true, force: true });
  }
});

test("Tencent TKE provider cleans up a partially-created runtime when shared Ingress route registration fails", async () => {
  const stateRootDir = await mkdtemp(join(tmpdir(), "opl-cloud-tke-state-"));
  const calls = [];
  const runner = async ({ command, args }) => {
    calls.push({ command, args });
    const commandLine = `${command} ${args.join(" ")}`;
    if (commandLine === "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json") {
      return JSON.stringify(sharedIngressFixture());
    }
    if (commandLine.endsWith("shared-ingress-route.k8s.json")) {
      throw new Error("shared_ingress_apply_failed");
    }
    return "";
  };
  const provider = new TencentTkeProvider({
    env: requiredEnv,
    runner,
    commandExists: () => true,
    stateRootDir
  });

  try {
    await assert.rejects(
      provider.createWorkspaceRuntime({
        workspaceId: "ws-tke404",
        ownerAccountId: "pi-alpha",
        workspaceName: "Route Failure Lab",
        packagePlan: { id: "basic", server: "2c4g", diskGb: 10 },
        token: "share_route_failure"
      }),
      /shared_ingress_apply_failed/
    );

    const commandLines = calls.map((call) => `${call.command} ${call.args.join(" ")}`);
    assert.deepEqual(commandLines, [
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${join(stateRootDir, "ws-tke404", "workspace.k8s.json")}`,
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json",
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${join(stateRootDir, "ws-tke404", "shared-ingress-route.k8s.json")}`,
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json",
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${join(stateRootDir, "ws-tke404", "shared-ingress-route.k8s.json")}`,
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud delete deployment/opl-ws-tke404 service/opl-ws-tke404 secret/opl-ws-tke404-env pvc/opl-ws-tke404-data --ignore-not-found=true"
    ]);
  } finally {
    await rm(stateRootDir, { recursive: true, force: true });
  }
});

test("Tencent TKE provider fails closed and cleans up when the Workspace runtime never becomes ready", async () => {
  const stateRootDir = await mkdtemp(join(tmpdir(), "opl-cloud-tke-state-"));
  const calls = [];
  let ingressReadCount = 0;
  const runner = async ({ command, args }) => {
    calls.push({ command, args });
    const commandLine = `${command} ${args.join(" ")}`;
    if (commandLine === "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json") {
      ingressReadCount += 1;
      return JSON.stringify(ingressReadCount === 1
        ? sharedIngressFixture()
        : sharedIngressFixture({
          workspacePaths: [{ path: "/w/ws-tke909", serviceName: "opl-ws-tke909" }]
        }));
    }
    if (commandLine === "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get deployment/opl-ws-tke909 pvc/opl-ws-tke909-data service/opl-ws-tke909 ingress/opl-cloud endpoints/opl-ws-tke909 -o json") {
      return JSON.stringify(runtimeStatusFixture({
        name: "opl-ws-tke909",
        workspaceId: "ws-tke909",
        image: requiredEnv.OPL_WORKSPACE_IMAGE,
        ready: false
      }));
    }
    return "";
  };
  const provider = new TencentTkeProvider({
    env: {
      ...requiredEnv,
      OPL_TKE_WORKSPACE_READY_TIMEOUT_MS: "0"
    },
    runner,
    commandExists: () => true,
    stateRootDir
  });

  try {
    await assert.rejects(
      provider.createWorkspaceRuntime({
        workspaceId: "ws-tke909",
        ownerAccountId: "pi-alpha",
        workspaceName: "Pending Runtime Lab",
        packagePlan: { id: "pro", accelerator: "cpu", cpu: 8, memoryGb: 16, server: "8c16g", diskGb: 100 },
        token: "share_pending_runtime"
      }),
      /tencent_tke_workspace_not_ready:deployment_ready,workspace_image_pulled,service_endpoints_ready/
    );

    const manifestPath = join(stateRootDir, "ws-tke909", "workspace.k8s.json");
    const routePath = join(stateRootDir, "ws-tke909", "shared-ingress-route.k8s.json");
    assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${manifestPath}`,
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json",
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${routePath}`,
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get deployment/opl-ws-tke909 pvc/opl-ws-tke909-data service/opl-ws-tke909 ingress/opl-cloud endpoints/opl-ws-tke909 -o json",
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json",
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${routePath}`,
      "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud delete deployment/opl-ws-tke909 service/opl-ws-tke909 secret/opl-ws-tke909-env pvc/opl-ws-tke909-data --ignore-not-found=true"
    ]);
  } finally {
    await rm(stateRootDir, { recursive: true, force: true });
  }
});

test("Tencent TKE provider still destroys compute when shared Ingress route cleanup fails", async () => {
  const calls = [];
  const provider = new TencentTkeProvider({
    env: requiredEnv,
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      const commandLine = `${command} ${args.join(" ")}`;
      if (commandLine === "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json") {
        return JSON.stringify(sharedIngressFixture({
          workspacePaths: [{ path: "/w/ws-tke505", serviceName: "opl-ws-tke505" }]
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
  const workspace = {
    id: "ws-tke505",
    server: { id: "deployment/opl-ws-tke505", status: "running", billingStatus: "active", spec: "2c4g" },
    docker: { image: requiredEnv.OPL_WORKSPACE_IMAGE },
    disk: { id: "pvc/opl-ws-tke505-data", status: "attached_retained", billingStatus: "active", sizeGb: 10 }
  };

  const destroyed = await provider.destroyServer({ workspace });

  assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get ingress/opl-cloud -o json",
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f .runtime/test-tke/ws-tke505/shared-ingress-route.k8s.json",
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud delete deployment/opl-ws-tke505 service/opl-ws-tke505 secret/opl-ws-tke505-env --ignore-not-found=true"
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
      return JSON.stringify({
        apiVersion: "v1",
        kind: "List",
        items: [
          {
            apiVersion: "apps/v1",
            kind: "Deployment",
            metadata: {
              name: "opl-ws-tke303",
              labels: {
                "app.kubernetes.io/name": "opl-workspace",
                "app.kubernetes.io/instance": "opl-ws-tke303",
                "oplcloud.cn/workspace-id": "ws-tke303"
              }
            },
            spec: {
              template: {
                metadata: {
                  labels: {
                    "app.kubernetes.io/name": "opl-workspace",
                    "app.kubernetes.io/instance": "opl-ws-tke303",
                    "oplcloud.cn/workspace-id": "ws-tke303"
                  }
                },
                spec: {
                  containers: [{ name: "workspace", image: requiredEnv.OPL_WORKSPACE_IMAGE }],
                  volumes: [{ name: "workspace-data", persistentVolumeClaim: { claimName: "opl-ws-tke303-data" } }]
                }
              }
            },
            status: { readyReplicas: 1, availableReplicas: 1 }
          },
          {
            apiVersion: "v1",
            kind: "PersistentVolumeClaim",
            metadata: { name: "opl-ws-tke303-data" },
            spec: { storageClassName: "cbs" },
            status: { phase: "Bound" }
          },
          {
            apiVersion: "v1",
            kind: "Service",
            metadata: { name: "opl-ws-tke303" },
            spec: {
              selector: {
                "app.kubernetes.io/name": "opl-workspace",
                "app.kubernetes.io/instance": "opl-ws-tke303",
                "oplcloud.cn/workspace-id": "ws-tke303"
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
                        path: "/w/ws-tke303",
                        backend: { service: { name: "opl-ws-tke303", port: { number: 3000 } } }
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
            metadata: { name: "opl-ws-tke303" },
            subsets: [{ addresses: [{ ip: "10.0.0.8" }], ports: [{ name: "http", port: 3000 }] }]
          }
        ]
      });
    },
    commandExists: () => true,
    stateRootDir: ".runtime/test-tke"
  });
  const workspace = {
    id: "ws-tke303",
    server: { id: "deployment/opl-ws-tke303" },
    docker: { id: "deployment/opl-ws-tke303", image: requiredEnv.OPL_WORKSPACE_IMAGE, service: "service/opl-ws-tke303" },
    disk: { id: "pvc/opl-ws-tke303-data", storageClass: "cbs" },
    access: { token: "share_runtime_status" },
    url: "https://workspace.medopl.cn/w/ws-tke303?token=share_runtime_status"
  };

  const status = await provider.runtimeStatus({ workspace });

  assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud get deployment/opl-ws-tke303 pvc/opl-ws-tke303-data service/opl-ws-tke303 ingress/opl-cloud endpoints/opl-ws-tke303 -o json"
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
    "ingress_routes_workspace_url:true"
  ]);
  assert.equal(status.resources.deployment.image, requiredEnv.OPL_WORKSPACE_IMAGE);
  assert.equal(status.resources.pvc.name, "opl-ws-tke303-data");
  assert.equal(status.resources.pvc.phase, "Bound");
  assert.equal(status.resources.service.name, "opl-ws-tke303");
  assert.equal(status.resources.ingress.name, "opl-cloud");
  assert.equal(status.resources.ingress.path, "/w/ws-tke303");
  assert.equal(status.resources.endpoints.readyAddresses, 1);
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
            "app.kubernetes.io/name": "opl-workspace",
            "app.kubernetes.io/instance": name,
            "oplcloud.cn/workspace-id": workspaceId
          }
        },
        spec: {
          template: {
            metadata: {
              labels: {
                "app.kubernetes.io/name": "opl-workspace",
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
            "app.kubernetes.io/name": "opl-workspace",
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
                    path: `/w/${workspaceId}`,
                    backend: { service: { name, port: { number: 3000 } } }
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
