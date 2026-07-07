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
		"Deployment",
		"Service",
		"Deployment",
		"Service",
		"TkeServiceConfig",
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
  assert.equal(config.data.OPL_TENCENT_PROVISIONER_BIN, "/usr/local/bin/opl-tencent-provisioner");
  assert.equal(config.data.TENCENT_DEPLOY_CLUSTER_ID, "cls-xxxxxxxx");
  assert.equal(config.data.TENCENTCLOUD_REGION, "ap-guangzhou");
  assert.equal(config.data.TENCENT_CVM_SUBNET_ID, "subnet-xxxxxxxx");
  assert.equal(config.data.TENCENT_CVM_SECURITY_GROUP_IDS, "sg-xxxxxxxx");
  assert.equal(config.data.RUN_TENCENT_CREATE_RELEASE_EXECUTION, "0");
  assert.equal(config.data.TENCENT_TCR_REGISTRY, "registry.example.com");
  assert.equal(config.data.TENCENT_TCR_NAMESPACE, "opl");
  assert.equal(config.data.TENCENT_TCR_REGION, "ap-guangzhou");
	assert.equal(config.data.OPL_CODEX_MODEL, "gpt-5.5");
	assert.equal(config.data.OPL_CODEX_REASONING_EFFORT, "xhigh");
	assert.equal(config.data.OPL_CODEX_BASE_URL, "https://gflabtoken.cn/v1");
	assert.equal(config.data.LEDGER_URL, "http://opl-cloud-ledger:8081");
	assert.equal(config.data.FABRIC_URL, "http://opl-cloud-fabric:8082");

	const deployments = items.filter((item) => item.kind === "Deployment");
	assert.deepEqual(deployments.map((item) => item.metadata.name), [
		"opl-cloud-control-plane",
		"opl-cloud-ledger",
		"opl-cloud-fabric"
	]);
	const deployment = deployments.find((item) => item.metadata.name === "opl-cloud-control-plane");
	assert.equal(deployment.metadata.name, "opl-cloud-control-plane");
	assert.deepEqual(deployment.spec.selector.matchLabels, { "app.kubernetes.io/name": "opl-cloud" });
	assert.equal(deployment.spec.template.metadata.labels["app.kubernetes.io/component"], "control-plane");
	assert.equal(deployment.spec.template.spec.serviceAccountName, "opl-cloud-control-plane");
  assert.equal(deployment.spec.template.spec.automountServiceAccountToken, false);
  assert.deepEqual(deployment.spec.template.spec.imagePullSecrets, [{ name: "tcr-pull-secret" }]);
  const container = deployment.spec.template.spec.containers[0];
  assert.match(container.image, /\/opl-cloud:/);
  assert.equal(container.ports[0].containerPort, 8787);
  assert.equal(container.readinessProbe.httpGet.path, "/api/production/readiness");
  assert.equal(container.livenessProbe.httpGet.path, "/api/healthz");
  assert.deepEqual(container.envFrom, [{ configMapRef: { name: "opl-cloud-config" } }]);
  assert.equal(container.env.find((item) => item.name === "PGSSLMODE").value, "disable");
  assert.deepEqual(container.env.filter((item) => item.valueFrom).map((item) => `${item.name}->${item.valueFrom.secretKeyRef.name}/${item.valueFrom.secretKeyRef.key}`), [
    "DATABASE_URL->opl-cloud-database/DATABASE_URL",
    "OPL_CONSOLE_USERS_JSON->opl-cloud-auth/OPL_CONSOLE_USERS_JSON",
    "OPL_OPERATOR_SUMMARY_TOKEN->opl-cloud-operator/OPL_OPERATOR_SUMMARY_TOKEN",
    "OPL_AIONUI_ADMIN_PASSWORD_SEED->opl-cloud-aionui/OPL_AIONUI_ADMIN_PASSWORD_SEED",
    "OPL_CODEX_API_KEY->opl-cloud-workspace-codex/OPL_CODEX_API_KEY"
  ]);
  assert.equal(container.env.find((item) => item.name === "OPL_OPERATOR_SUMMARY_TOKEN").valueFrom.secretKeyRef.optional, true);
  assert.equal(container.env.find((item) => item.name === "OPL_CODEX_API_KEY").valueFrom.secretKeyRef.optional, true);
	assert.deepEqual(deployment.spec.template.spec.volumes.map((volume) => volume.name), [
		"runtime-state",
		"tmp"
	]);
	const ledger = deployments.find((item) => item.metadata.name === "opl-cloud-ledger");
	const ledgerMigration = ledger.spec.template.spec.initContainers[0];
	assert.equal(ledgerMigration.name, "ledger-schema-migration");
	assert.equal(ledgerMigration.command[0], "node");
	assert.match(ledgerMigration.args.join(" "), /ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS reason TEXT/);
	assert.match(ledgerMigration.args.join(" "), /ALTER TABLE wallet_transactions ADD COLUMN IF NOT EXISTS ledger_entry_id TEXT/);
	assert.match(ledgerMigration.args.join(" "), /ALTER TABLE wallet_transactions DROP COLUMN IF EXISTS user_id/);
	assert.match(ledgerMigration.args.join(" "), /ALTER TABLE wallet_transactions DROP COLUMN IF EXISTS transaction_type/);
	assert.match(ledgerMigration.args.join(" "), /ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS account_id TEXT/);
	assert.match(ledgerMigration.args.join(" "), /UPDATE manual_topups SET account_id = target_account_id/);
	assert.match(ledgerMigration.args.join(" "), /ALTER TABLE manual_topups ALTER COLUMN account_id SET NOT NULL/);
	assert.match(ledgerMigration.args.join(" "), /ALTER TABLE manual_topups DROP COLUMN IF EXISTS target_account_id/);
	assert.equal(ledgerMigration.env.find((item) => item.name === "PGSSLMODE").value, "disable");
	assert.deepEqual(ledgerMigration.env.filter((item) => item.valueFrom).map((item) => `${item.name}->${item.valueFrom.secretKeyRef.name}/${item.valueFrom.secretKeyRef.key}`), [
		"DATABASE_URL->opl-cloud-database/DATABASE_URL"
	]);
	assert.equal(ledger.spec.template.spec.containers[0].command[0], "/usr/local/bin/opl-ledger");
	assert.equal(ledger.spec.template.spec.containers[0].imagePullPolicy, "Always");
	assert.equal(ledger.spec.template.spec.containers[0].ports[0].containerPort, 8081);
	assert.equal(ledger.spec.template.spec.containers[0].env.find((item) => item.name === "PGSSLMODE").value, "disable");
	assert.deepEqual(ledger.spec.template.spec.containers[0].env.filter((item) => item.valueFrom).map((item) => `${item.name}->${item.valueFrom.secretKeyRef.name}/${item.valueFrom.secretKeyRef.key}`), [
		"DATABASE_URL->opl-cloud-database/DATABASE_URL"
	]);
	const fabric = deployments.find((item) => item.metadata.name === "opl-cloud-fabric");
	assert.equal(fabric.spec.template.spec.containers[0].command[0], "/usr/local/bin/opl-fabric");
	assert.equal(fabric.spec.template.spec.containers[0].ports[0].containerPort, 8082);
	assert.deepEqual(fabric.spec.template.spec.containers[0].env.map((item) => `${item.name}->${item.valueFrom.secretKeyRef.name}/${item.valueFrom.secretKeyRef.key}`), [
		"TENCENTCLOUD_SECRET_ID->opl-cloud-tencent-mutation/TENCENTCLOUD_SECRET_ID",
		"TENCENTCLOUD_SECRET_KEY->opl-cloud-tencent-mutation/TENCENTCLOUD_SECRET_KEY"
	]);
	assert.deepEqual(fabric.spec.template.spec.containers[0].volumeMounts, [
		{ name: "deploy-kubeconfig", mountPath: "/var/run/opl-cloud/kubeconfig", readOnly: true }
	]);
	assert.deepEqual(fabric.spec.template.spec.volumes, [
		{ name: "deploy-kubeconfig", secret: { secretName: "opl-cloud-deploy-kubeconfig" } }
	]);
	const services = items.filter((item) => item.kind === "Service");
	assert.deepEqual(services.map((item) => item.metadata.name), [
		"opl-cloud-control-plane",
		"opl-cloud-ledger",
		"opl-cloud-fabric"
	]);
	for (const service of services) {
		assert.equal(service.spec.selector["app.kubernetes.io/component"], service.metadata.labels["app.kubernetes.io/component"]);
	}

  const ingress = items.find((item) => item.kind === "Ingress");
  assert.equal(ingress.spec.ingressClassName, "qcloud");
  assert.equal(ingress.metadata.annotations["ingress.cloud.tencent.com/direct-access"], "true");
  assert.equal(ingress.metadata.annotations["ingress.cloud.tencent.com/tke-service-config"], "opl-cloud-ingress-config");
  assert.deepEqual(ingress.spec.tls, [
    { hosts: ["cloud.medopl.cn"], secretName: "opl-cloud-console-medopl-cn-tls" },
    { hosts: ["workspace.medopl.cn"], secretName: "opl-cloud-workspace-medopl-cn-tls" }
  ]);
  assert.deepEqual(ingress.spec.rules.map((rule) => rule.host), ["cloud.medopl.cn", "workspace.medopl.cn"]);

  const serviceConfig = items.find((item) => item.kind === "TkeServiceConfig");
  assert.equal(serviceConfig.metadata.name, "opl-cloud-ingress-config");
  assert.deepEqual(serviceConfig.spec.loadBalancer.l7Listeners, [
    {
      protocol: "HTTPS",
      port: 443,
      domains: [
        {
          domain: "workspace.medopl.cn",
          http2: false,
          rules: [{ url: "/", forwardType: "HTTP" }]
        }
      ]
    }
  ]);
});
