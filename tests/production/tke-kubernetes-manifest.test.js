import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("OPL Cloud TKE manifest declares the control plane, routing, and secret refs", async () => {
  const source = await readFile("deploy/tke/opl-cloud.k8s.json", "utf8");
  assert.equal(source.includes("postgresql://"), false);

  const manifest = JSON.parse(source);
  assert.equal(manifest.kind, "List");
  const items = manifest.items;
  assert.deepEqual(items.map((item) => item.kind), [
    "Namespace",
    "ServiceAccount",
    "ConfigMap",
    "Deployment",
    "Service",
    "NetworkPolicy",
    "Ingress"
  ]);

  const namespace = items.find((item) => item.kind === "Namespace");
  assert.equal(namespace.metadata.name, "opl-cloud");

  const config = items.find((item) => item.kind === "ConfigMap");
  assert.equal(config.metadata.name, "opl-cloud-config");
  assert.equal(config.data.OPL_RUNTIME_PROVIDER, "tencent-tke");
  assert.equal(config.data.OPL_PUBLIC_URL, "https://cloud.medopl.cn");
  assert.equal(config.data.OPL_WORKSPACE_DOMAIN, "workspace.medopl.cn");
  assert.match(config.data.OPL_CLOUD_IMAGE, /\/opl-cloud:/);
  assert.match(config.data.OPL_WORKSPACE_IMAGE, /\/one-person-lab-app:/);
  assert.equal(config.data.OPL_WORKSPACE_VOLUME_SNAPSHOT_CLASS, "cbs-snapshot");
  assert.equal(config.data.TENCENT_DEPLOY_CLUSTER_ID, "cls-xxxxxxxx");
  assert.equal(config.data.TENCENT_TCR_REGISTRY, "registry.example.com");
  assert.equal(config.data.TENCENT_TCR_NAMESPACE, "opl");
  assert.equal(config.data.TENCENT_TCR_REGION, "ap-guangzhou");

  const deployment = items.find((item) => item.kind === "Deployment");
  assert.equal(deployment.metadata.name, "opl-cloud-control-plane");
  assert.equal(deployment.spec.template.spec.serviceAccountName, "opl-cloud-control-plane");
  assert.equal(deployment.spec.template.spec.automountServiceAccountToken, false);
  assert.deepEqual(deployment.spec.template.spec.imagePullSecrets, [{ name: "tcr-pull-secret" }]);
  const container = deployment.spec.template.spec.containers[0];
  assert.match(container.image, /\/opl-cloud:/);
  assert.equal(container.ports[0].containerPort, 8787);
  assert.equal(container.readinessProbe.httpGet.path, "/api/production/readiness");
  assert.equal(container.livenessProbe.httpGet.path, "/api/healthz");
  assert.deepEqual(container.envFrom, [{ configMapRef: { name: "opl-cloud-config" } }]);
  assert.deepEqual(container.env.filter((item) => item.valueFrom).map((item) => `${item.name}->${item.valueFrom.secretKeyRef.name}/${item.valueFrom.secretKeyRef.key}`), [
    "DATABASE_URL->opl-cloud-database/DATABASE_URL",
    "OPL_CONSOLE_USERS_JSON->opl-cloud-auth/OPL_CONSOLE_USERS_JSON",
    "OPL_OPERATOR_SUMMARY_TOKEN->opl-cloud-operator/OPL_OPERATOR_SUMMARY_TOKEN"
  ]);
  assert.equal(container.env.find((item) => item.name === "OPL_OPERATOR_SUMMARY_TOKEN").valueFrom.secretKeyRef.optional, true);
  assert.deepEqual(deployment.spec.template.spec.volumes.map((volume) => volume.name), [
    "runtime-state",
    "deploy-kubeconfig",
    "tmp"
  ]);

  const ingress = items.find((item) => item.kind === "Ingress");
  assert.equal(ingress.spec.ingressClassName, "qcloud");
  assert.equal(ingress.metadata.annotations["ingress.cloud.tencent.com/direct-access"], "true");
  assert.deepEqual(ingress.spec.tls, [
    { hosts: ["cloud.medopl.cn"], secretName: "opl-cloud-console-medopl-cn-tls" },
    { hosts: ["workspace.medopl.cn"], secretName: "opl-cloud-workspace-medopl-cn-tls" }
  ]);
  assert.deepEqual(ingress.spec.rules.map((rule) => rule.host), ["cloud.medopl.cn", "workspace.medopl.cn"]);
});
