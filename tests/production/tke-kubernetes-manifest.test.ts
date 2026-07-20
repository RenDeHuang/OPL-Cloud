import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
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
  assert.equal(config.data.OPL_GATEWAY_PUBLIC_BASE_URL, undefined);
  assert.equal(config.data.OPL_CODEX_BASE_URL, undefined);
  assert.equal(config.data.OPL_OPERATOR_CIDRS, undefined);
  assert.equal(config.data.OPL_TRUSTED_PROXY_CIDRS, undefined);
  assert.equal(config.data.OPL_BASIC_COMPUTE_NODE_POOL_ID, undefined);
  assert.equal(config.data.OPL_PRO_COMPUTE_NODE_POOL_ID, undefined);
  assert.equal(config.data.OPL_COMPUTE_LAUNCH_ZONE, undefined);
  assert.equal(config.data.OPL_SUB2API_REQUEST_TIMEOUT_MS, "5000");
  assert.equal(config.data.TENCENTCLOUD_REGION, "na-siliconvalley");
  assert.equal(config.data.OPL_TENCENT_ZONE, "na-siliconvalley-1");
  assert.match(config.data.OPL_CLOUD_IMAGE, /^[^:@]+(?:\/[^:@]+)+@sha256:[0-9a-f]{64}$/);
  assert.match(config.data.OPL_WORKSPACE_IMAGE, /^[^:@]+(?:\/[^:@]+)+@sha256:[0-9a-f]{64}$/);
  assert.equal(config.data.OPL_BASIC_COMPUTE_HOURLY_CNY, undefined);
  assert.equal(config.data.OPL_RESOURCE_BILLING_WORKER_ENABLED, undefined);

  const controlPlane = deployments.find((item) => item.metadata.name === "opl-cloud-control-plane");
  assert.equal(controlPlane.spec.replicas, 1);
  assert.equal(controlPlane.spec.strategy.type, "Recreate");
  const controlContainer = controlPlane.spec.template.spec.containers[0];
  assert.deepEqual(controlContainer.envFrom, [{ configMapRef: { name: "opl-cloud-config" } }]);
  assert.equal(controlContainer.readinessProbe.httpGet.path, "/api/production/readiness");
  assert.equal(controlContainer.livenessProbe.httpGet.path, "/api/healthz");
  assert.deepEqual(controlContainer.env.filter((item) => item.valueFrom).map((item) => item.name), [
    "DATABASE_URL",
    "OPL_INTERNAL_SERVICE_TOKEN",
    "OPL_AIONUI_ADMIN_PASSWORD_SEED",
    "OPL_SUB2API_ADMIN_EMAIL",
    "OPL_SUB2API_ADMIN_PASSWORD"
  ]);
  assert.equal(source.includes("OPL_OPERATOR_SUMMARY_TOKEN"), false);
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

test("TKE workloads and internal services enforce native isolation", async () => {
  const manifest = JSON.parse(await readFile("deploy/tke/opl-cloud.k8s.json", "utf8"));
  const deployments = manifest.items.filter((item) => item.kind === "Deployment");
  const policies = manifest.items.filter((item) => item.kind === "NetworkPolicy");

  for (const deployment of deployments) {
    const pod = deployment.spec.template.spec;
    assert.equal(pod.securityContext.runAsNonRoot, true, deployment.metadata.name);
    assert.equal(pod.securityContext.seccompProfile.type, "RuntimeDefault", deployment.metadata.name);
    for (const container of [...(pod.initContainers || []), ...pod.containers]) {
      assert.equal(container.securityContext.allowPrivilegeEscalation, false, container.name);
      assert.deepEqual(container.securityContext.capabilities.drop, ["ALL"], container.name);
      assert.equal(container.env?.some((entry) => entry.name === "PGSSLMODE"), false, container.name);
    }
  }

  for (const [component, port] of [["ledger", 8081], ["fabric", 8082]]) {
    const policy = policies.find((item) => item.spec.podSelector?.matchLabels?.["app.kubernetes.io/component"] === component);
    assert.ok(policy, `${component} NetworkPolicy missing`);
    assert.deepEqual(policy.spec.policyTypes, ["Ingress"]);
    assert.deepEqual(policy.spec.ingress, [{
      from: [{ podSelector: { matchLabels: {
        "app.kubernetes.io/name": "opl-cloud",
        "app.kubernetes.io/component": "control-plane"
      } } }],
      ports: [{ protocol: "TCP", port }]
    }]);
  }
});

test("ordinary deploy omits retired network, static pool, and Gateway URL variables", async () => {
  const [manifest, deployment, management, renderer, workflow] = await Promise.all([
    readFile("deploy/tke/opl-cloud.k8s.json", "utf8").then(JSON.parse),
    readFile("packages/contracts/opl-cloud-deployment-contract.json", "utf8").then(JSON.parse),
    readFile("packages/contracts/opl-cloud-management-contract.json", "utf8").then(JSON.parse),
    readFile("tools/render-tke-manifest.ts", "utf8"),
    readFile(".github/workflows/deploy-tke-production.yml", "utf8")
  ]);
  const config = manifest.items.find((item) => item.kind === "ConfigMap");
  for (const key of [
    "OPL_OPERATOR_CIDRS",
    "OPL_TRUSTED_PROXY_CIDRS",
    "OPL_BASIC_COMPUTE_NODE_POOL_ID",
    "OPL_PRO_COMPUTE_NODE_POOL_ID",
    "OPL_GATEWAY_PUBLIC_BASE_URL",
    "OPL_CODEX_BASE_URL"
  ]) {
    assert.equal(config.data[key], undefined, key);
    assert.equal(deployment.deployWorkflow.requiredEnv.includes(key), false, key);
    assert.doesNotMatch(renderer, new RegExp(`"${key}"`));
    assert.doesNotMatch(workflow, new RegExp(`${key}:`));
  }
  assert.equal(management.operatorAuthPolicy.productionNetworkGate, undefined);
});

test("production env examples use the launch zone and pinned images", async () => {
  const paths = [
    ".env.example",
    "deploy/tke/opl-cloud-production.env.example",
    "deploy/tke/opl-cloud-staging.local.env.example"
  ];
  for (const path of paths) {
    const source = await readFile(path, "utf8");
    assert.match(source, /^OPL_TENCENT_ZONE=na-siliconvalley-1$/m, path);
    assert.doesNotMatch(source, /^OPL_WORKSPACE_IMAGE=.*(?::latest|:<tag>)$/m, path);
  }
  for (const path of paths.slice(1)) {
    const source = await readFile(path, "utf8");
    assert.match(source, /^OPL_WORKSPACE_IMAGE=.*@sha256:/m, path);
    assert.doesNotMatch(source, /^OPL_GATEWAY_PUBLIC_BASE_URL=/m, path);
    assert.doesNotMatch(source, /^OPL_CODEX_BASE_URL=/m, path);
    assert.match(source, /^TENCENTCLOUD_REGION=na-siliconvalley$/m, path);
    assert.match(source, /^DATABASE_URL=.*sslmode=verify-full$/m, path);
    assert.doesNotMatch(source, /^OPL_OPERATOR_CIDRS=/m, path);
    assert.doesNotMatch(source, /^OPL_TRUSTED_PROXY_CIDRS=/m, path);
    assert.doesNotMatch(source, /^OPL_(?:BASIC|PRO)_COMPUTE_NODE_POOL_ID=/m, path);
    assert.doesNotMatch(source, /^OPL_OPERATOR_SUMMARY_TOKEN=/m, path);
  }
  assert.match(await readFile(".env.example", "utf8"), /^OPL_WORKSPACE_IMAGE=$/m);
});

test("production automation does not retain the legacy operator cleanup path", async () => {
  const workflowDirectory = ".github/workflows";
  const workflowNames = (await readdir(workflowDirectory)).filter((name) => /\.ya?ml$/.test(name));
  for (const name of workflowNames) {
    const source = await readFile(`${workflowDirectory}/${name}`, "utf8");
    assert.equal(source.includes("/api/auth/operator-login"), false, `${name} invokes the legacy operator login`);
    assert.equal(source.includes("OPL_OPERATOR_SUMMARY_TOKEN"), false, `${name} uses the legacy operator token`);
  }
  assert.equal(workflowNames.includes("cleanup-console-resource-residual.yml"), false);
  assert.doesNotMatch(await readFile(".env.example", "utf8"), /^OPL_OPERATOR_SUMMARY_TOKEN=/m);
});
