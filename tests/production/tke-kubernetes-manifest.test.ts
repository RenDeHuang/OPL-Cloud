import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("OPL Cloud TKE manifest declares three decoupled services and monthly Sub2API config", async () => {
  const source = await readFile("deploy/tke/opl-cloud.k8s.json", "utf8");
  const manifest = JSON.parse(source);
  const config = manifest.items.find((item) => item.kind === "ConfigMap");
  const deployments = manifest.items.filter((item) => item.kind === "Deployment");

  assert.equal(manifest.kind, "List");
  assert.equal(source.includes("postgresql://"), false);
  assert.deepEqual(deployments.map((item) => item.metadata.name), [
    "opl-cloud-control-plane",
    "opl-cloud-ledger",
    "opl-cloud-fabric"
  ]);
  assert.equal(config.data.OPL_RUNTIME_PROVIDER, "tencent-tke");
  assert.equal(config.data.OPL_MONTHLY_BILLING_WORKER_ENABLED, "1");
  assert.equal(config.data.OPL_MONTHLY_BILLING_INTERVAL_MS, "3600000");
  assert.equal(config.data.OPL_SUB2API_BASE_URL, "https://gflabtoken.cn");
  assert.equal(config.data.OPL_SUB2API_SUPPORTED_VERSIONS, "0.1.156,0.1.155");
  assert.equal(config.data.OPL_SUB2API_REQUEST_TIMEOUT_MS, "5000");
  assert.equal(config.data.TENCENTCLOUD_REGION, "na-siliconvalley");
  assert.equal(config.data.OPL_TENCENT_ZONE, "na-siliconvalley-1");
  assert.match(config.data.OPL_CLOUD_IMAGE, /^[^:@]+(?:\/[^:@]+)+@sha256:[0-9a-f]{64}$/);
  assert.match(config.data.OPL_WORKSPACE_IMAGE, /^[^:@]+(?:\/[^:@]+)+@sha256:[0-9a-f]{64}$/);
  assert.equal(config.data.OPL_BASIC_COMPUTE_HOURLY_CNY, undefined);
  assert.equal(config.data.OPL_RESOURCE_BILLING_WORKER_ENABLED, undefined);

  const controlPlane = deployments.find((item) => item.metadata.name === "opl-cloud-control-plane");
  assert.equal(controlPlane.spec.strategy.type, "Recreate");
  const controlContainer = controlPlane.spec.template.spec.containers[0];
  assert.deepEqual(controlContainer.envFrom, [{ configMapRef: { name: "opl-cloud-config" } }]);
  assert.equal(controlContainer.readinessProbe.httpGet.path, "/api/production/readiness");
  assert.equal(controlContainer.livenessProbe.httpGet.path, "/api/healthz");
  assert.deepEqual(controlContainer.env.filter((item) => item.valueFrom).map((item) => item.name), [
    "DATABASE_URL",
    "OPL_INTERNAL_SERVICE_TOKEN",
    "OPL_CONSOLE_USERS_JSON",
    "OPL_OPERATOR_SUMMARY_TOKEN",
    "OPL_AIONUI_ADMIN_PASSWORD_SEED",
    "OPL_SUB2API_ADMIN_EMAIL",
    "OPL_SUB2API_ADMIN_PASSWORD"
  ]);
  assert.equal(source.includes("OPL_CODEX_API_KEY"), false);

  const ledger = deployments.find((item) => item.metadata.name === "opl-cloud-ledger");
  assert.equal(ledger.spec.template.spec.initContainers, undefined);
  assert.equal(ledger.spec.template.spec.containers[0].command[0], "/usr/local/bin/opl-ledger");

  const fabric = deployments.find((item) => item.metadata.name === "opl-cloud-fabric");
  assert.equal(fabric.spec.template.spec.containers[0].command[0], "/usr/local/bin/opl-fabric");
  assert.deepEqual(fabric.spec.template.spec.volumes, [
    { name: "deploy-kubeconfig", secret: { secretName: "opl-cloud-deploy-kubeconfig" } }
  ]);

  const ingress = manifest.items.find((item) => item.kind === "Ingress");
  assert.equal(ingress.spec.ingressClassName, "qcloud");
  assert.deepEqual(ingress.spec.rules.map((rule) => rule.host), ["cloud.medopl.cn", "workspace.medopl.cn"]);
});

test("production env examples use the frozen Sub2API versions and launch zone", async () => {
  const paths = [
    ".env.example",
    "deploy/tke/opl-cloud-production.env.example",
    "deploy/tke/opl-cloud-staging.local.env.example"
  ];
  for (const path of paths) {
    const source = await readFile(path, "utf8");
    assert.match(source, /^OPL_SUB2API_SUPPORTED_VERSIONS=0\.1\.156,0\.1\.155$/m, path);
    assert.match(source, /^OPL_TENCENT_ZONE=na-siliconvalley-1$/m, path);
    assert.match(source, /^OPL_WORKSPACE_IMAGE=.*@sha256:/m, path);
    assert.doesNotMatch(source, /^OPL_WORKSPACE_IMAGE=.*(?::latest|:<tag>)$/m, path);
  }
  for (const path of paths.slice(1)) {
    assert.match(await readFile(path, "utf8"), /^TENCENTCLOUD_REGION=na-siliconvalley$/m, path);
  }
  assert.match(
    await readFile(".env.example", "utf8"),
    /^OPL_WORKSPACE_IMAGE=ghcr\.io\/gaofeng21cn\/one-person-lab-webui@sha256:9d867fe0fc9db48b6efa27371d77770e46fc8cd97d26ef85a81fbdac7e96ca76$/m
  );
});

test("production manifest example uses the frozen Sub2API versions", async () => {
  const manifest = JSON.parse(await readFile("deploy/production-manifest.example.json", "utf8"));
  assert.equal(manifest.env.OPL_SUB2API_SUPPORTED_VERSIONS.value, "0.1.156,0.1.155");
});
