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

test("Tencent TKE provider applies one Deployment, Service, PVC, Secret, and Ingress per Workspace", async () => {
  const stateRootDir = await mkdtemp(join(tmpdir(), "opl-cloud-tke-state-"));
  const calls = [];
  const runner = async ({ command, args, cwd, env }) => {
    calls.push({ command, args, cwd, env });
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
      packagePlan: { id: "basic", server: "2c4g", diskGb: 10 },
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
    const commandLines = calls.map((call) => `${call.command} ${call.args.join(" ")}`);
    assert.equal(commandLines.join("\n").includes("share_tke_secret"), false);
    assert.deepEqual(commandLines, [
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${manifestPath}`
    ]);

    const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
    assert.equal(manifest.kind, "List");
    assert.deepEqual(manifest.items.map((item) => item.kind), [
      "Secret",
      "PersistentVolumeClaim",
      "Deployment",
      "Service",
      "Ingress"
    ]);
    const deployment = manifest.items.find((item) => item.kind === "Deployment");
    const container = deployment.spec.template.spec.containers[0];
    assert.equal(container.image, requiredEnv.OPL_WORKSPACE_IMAGE);
    assert.deepEqual(deployment.spec.template.spec.imagePullSecrets, [{ name: "tcr-pull-secret" }]);
    assert.deepEqual(deployment.spec.template.spec.nodeSelector, { "medopl.cn/workload": "medopl" });
    assert.equal(container.ports[0].containerPort, 3000);
    assert.deepEqual(container.volumeMounts.map((mount) => `${mount.mountPath}:${mount.subPath}`), [
      "/data:data",
      "/projects:projects"
    ]);
    const ingress = manifest.items.find((item) => item.kind === "Ingress");
    assert.equal(ingress.spec.ingressClassName, "qcloud");
    assert.equal(ingress.spec.rules[0].host, "workspace.medopl.cn");
    assert.equal(ingress.spec.rules[0].http.paths[0].path, "/w/ws-tke001");
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
    "kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud delete deployment/opl-ws-tke101 service/opl-ws-tke101 ingress/opl-ws-tke101 secret/opl-ws-tke101-env --ignore-not-found=true",
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

    assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
      `kubectl --kubeconfig /tmp/kubeconfig --namespace opl-cloud apply -f ${manifestPath}`
    ]);
    assert.equal(server.id, "deployment/opl-ws-tke202");
    assert.equal(server.status, "running");
    const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
    const pvc = manifest.items.find((item) => item.kind === "PersistentVolumeClaim");
    assert.equal(pvc.metadata.name, "opl-ws-tke202-data");
    assert.equal(pvc.spec.resources.requests.storage, "10Gi");
  } finally {
    await rm(stateRootDir, { recursive: true, force: true });
  }
});
