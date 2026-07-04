import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parse } from "yaml";

import { renderTkeManifest } from "../../tools/render-tke-manifest.js";

const deploymentContractPath = new URL("../../packages/contracts/opl-cloud-deployment-contract.json", import.meta.url);
const pricingContractPath = new URL("../../packages/contracts/opl-cloud-pricing-contract.json", import.meta.url);

async function readJson(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

async function readWorkflow(path) {
  return parse(await readFile(path, "utf8"));
}

function job(workflow, name) {
  const current = workflow.jobs?.[name];
  assert.ok(current, `workflow missing job ${name}`);
  return current;
}

function stepsByName(currentJob) {
  return new Map((currentJob.steps || []).map((step) => [step.name, step]));
}

function serializedStep(step) {
  return `${step.run || ""}\n${JSON.stringify({ ...step, run: undefined })}`;
}

function serializedRuns(currentJob) {
  return (currentJob.steps || []).map((step) => step.run || "").join("\n");
}

function assertWorkflowContract(workflow, spec, rootContract) {
  const currentJob = job(workflow, spec.job);
  assert.deepEqual([currentJob["runs-on"]].flat(), spec.runner || rootContract.runner);
  assert.equal(currentJob.environment, rootContract.environment);

  const workflowInputs = Object.keys(workflow.on?.workflow_dispatch?.inputs || {});
  for (const input of spec.inputs || []) {
    assert.ok(workflowInputs.includes(input), `${spec.file} missing input ${input}`);
  }

  const stepMap = stepsByName(currentJob);
  assert.deepEqual([...stepMap.keys()], spec.steps);
  for (const [first, second] of spec.orderedSteps || []) {
    assert.ok(spec.steps.indexOf(first) < spec.steps.indexOf(second), `${first} must run before ${second}`);
  }

  for (const key of spec.requiredEnv || []) {
    assert.ok(Object.hasOwn(currentJob.env || {}, key), `${spec.file} missing env ${key}`);
  }
  for (const key of spec.secretEnv || []) {
    assert.ok(String(currentJob.env[key] || "").includes("secrets."), `${key} must come from GitHub secrets`);
  }

  for (const [stepName, tokens] of Object.entries(spec.requiredCommandsByStep || {})) {
    const step = stepMap.get(stepName);
    assert.ok(step, `${spec.file} missing step ${stepName}`);
    const text = serializedStep(step);
    for (const token of tokens) {
      assert.ok(text.includes(token), `${spec.file} ${stepName} missing ${token}`);
    }
  }

  const workflowText = JSON.stringify(workflow);
  const runText = serializedRuns(currentJob);
  for (const token of spec.forbiddenRunTokens || []) {
    assert.equal(workflowText.includes(token), false, `${spec.file} must not contain ${token}`);
  }
  for (const pattern of spec.forbiddenRunPatterns || []) {
    assert.doesNotMatch(runText, new RegExp(pattern), `${spec.file} must not match ${pattern}`);
  }
}

async function pricingContractValues() {
  const contract = await readJson(pricingContractPath);
  return {
    contract,
    env: {
      [contract.env.basicComputeHourly]: String(contract.computeHourly.basic),
      [contract.env.proComputeHourly]: String(contract.computeHourly.pro),
      [contract.env.storageGbMonth]: String(contract.storageGbMonth),
      [contract.env.markup]: String(contract.markup)
    }
  };
}

test("TKE production deploy workflow matches the deployment contract", async () => {
  const contract = await readJson(deploymentContractPath);
  const workflow = await readWorkflow(contract.deployWorkflow.file);
  assertWorkflowContract(workflow, contract.deployWorkflow, contract);
});

test("TKE old Workspace cleanup workflow is explicit and scoped to retired runtime objects", async () => {
  const contract = await readJson(deploymentContractPath);
  const workflow = await readWorkflow(contract.cleanupWorkflow.file);
  assertWorkflowContract(workflow, contract.cleanupWorkflow, contract);

  const currentJob = job(workflow, contract.cleanupWorkflow.job);
  const text = serializedRuns(currentJob);
  assert.match(text, /confirm=CLEAN_OLD_WORKSPACES|CONFIRM" != "CLEAN_OLD_WORKSPACES"/);
  assert.match(text, /app\.kubernetes\.io\/name=opl-workspace/);
  assert.match(text, /\^opl-ws-/);
  assert.match(text, /backend\?\.service\?\.name/);
  assert.match(text, /"deployment\/\$name"/);
  assert.match(text, /"service\/\$name"/);
  assert.match(text, /"secret\/\$name-env"/);
  assert.match(text, /"persistentvolumeclaim\/\$name-data"/);
  assert.match(text, /get deploy,svc,secret,pvc -o json/);
  assert.match(text, /kind === "Deployment" \|\| kind === "Service"/);
  assert.match(text, /kind === "Secret"/);
  assert.match(text, /kind === "PersistentVolumeClaim"/);
  assert.match(text, /name\.replace\(\/-env\$\/, ""\)/);
  assert.match(text, /name\.replace\(\/-data\$\/, ""\)/);
  assert.match(text, /namespace_confirm=DELETE_MEDOPL_AND_OPL_WEBUI|NAMESPACE_CONFIRM" != "DELETE_MEDOPL_AND_OPL_WEBUI"/);
  assert.match(text, /case "\$namespace" in\s+medopl\|opl-webui/s);
  assert.match(text, /delete deploy,sts,ds,job,cronjob/);
  assert.match(text, /delete pod\s+\\\n\s+--all/);
  assert.match(text, /cleanup_failed: namespace\/\$namespace still has pods before namespace deletion/);
  assert.match(text, /kubectl --kubeconfig "\$KUBECONFIG" delete namespace "\$namespace" --wait=true --timeout=300s/);
  assert.match(text, /remaining_runtime_objects=/);
  assert.match(text, /get deploy,svc,secret,pvc -o name/);
  assert.match(text, /\^\(deployment\.apps\|service\|secret\|persistentvolumeclaim\)\/opl-ws-/);
  assert.doesNotMatch(text, /delete namespace --all/);
  assert.doesNotMatch(text, /kubectl .* delete\s+ingress/);
  assert.doesNotMatch(text, /docker\s+rmi|tccli\s+tcr\s+Delete/);
});

test("TKE deploy can roll forward with an existing auth seed secret", async () => {
  const contract = await readJson(deploymentContractPath);
  const workflow = await readWorkflow(contract.deployWorkflow.file);
  const currentJob = job(workflow, contract.deployWorkflow.job);
  const stepMap = stepsByName(currentJob);

  assert.doesNotMatch(
    serializedStep(stepMap.get("Check deployment inputs")),
    /OPL_CONSOLE_USERS_JSON/,
    "deploy input guard must not require a fresh auth seed when a persisted K8s auth secret already exists"
  );
  assert.match(
    serializedStep(stepMap.get("Install Kubernetes secrets")),
    /get secret "?\$?OPL_AUTH_SECRET_NAME"?|get secret opl-cloud-auth/,
    "deploy must verify an existing auth seed secret before reusing it"
  );
  assert.match(
    serializedStep(stepMap.get("Install Kubernetes secrets")),
    /if \[ -n "\$\{OPL_CONSOLE_USERS_JSON:-\}" \]/,
    "deploy must still install a provided production auth seed without printing it"
  );
});

test("TKE production deploy workflow defaults to the versioned pricing contract", async () => {
  const deploymentContract = await readJson(deploymentContractPath);
  const workflow = await readWorkflow(deploymentContract.deployWorkflow.file);
  const currentJob = job(workflow, deploymentContract.deployWorkflow.job);
  const { env } = await pricingContractValues();

  for (const [key, value] of Object.entries(env)) {
    assert.ok(String(currentJob.env[key]).includes(`'${value}'`), `${key} default should match pricing contract`);
  }
  assert.equal(Object.hasOwn(currentJob.env, "OPL_GPU_COMPUTE_HOURLY_CNY"), false);
});

test("TKE manifest renderer replaces deploy-time values without rendering secrets", async () => {
  const source = await readFile("deploy/tke/opl-cloud.k8s.json", "utf8");
  const manifest = JSON.parse(source);
  const { env } = await pricingContractValues();
  const rendered = renderTkeManifest({
    manifest,
    values: {
      OPL_K8S_NAMESPACE: "opl-cloud",
      OPL_PUBLIC_URL: "https://cloud.medopl.cn",
      OPL_CONSOLE_DOMAIN: "cloud.medopl.cn",
      OPL_WORKSPACE_DOMAIN: "workspace.medopl.cn",
      OPL_CLOUD_IMAGE: "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud:test",
      OPL_WORKSPACE_IMAGE: "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:latest",
      OPL_IMAGE_PULL_SECRET_NAME: "tcr-pull-secret",
      OPL_WORKSPACE_STORAGE_CLASS: "cbs",
      OPL_BILLING_MARKUP: env.OPL_BILLING_MARKUP,
      OPL_BASIC_COMPUTE_HOURLY_CNY: env.OPL_BASIC_COMPUTE_HOURLY_CNY,
      OPL_PRO_COMPUTE_HOURLY_CNY: env.OPL_PRO_COMPUTE_HOURLY_CNY,
      OPL_STORAGE_GB_MONTH_CNY: env.OPL_STORAGE_GB_MONTH_CNY,
      OPL_CODEX_MODEL: "gpt-5.5",
      OPL_CODEX_REASONING_EFFORT: "xhigh",
      OPL_CODEX_BASE_URL: "https://gflabtoken.cn/v1",
      OPL_CONSOLE_TLS_SECRET_NAME: "opl-cloud-console-medopl-cn-tls",
      OPL_WORKSPACE_TLS_SECRET_NAME: "opl-cloud-workspace-medopl-cn-tls",
      OPL_INGRESS_CLASS: "qcloud",
      TENCENT_DEPLOY_CLUSTER_ID: "cls-oplcloud",
      TENCENT_TKE_REGION: "na-siliconvalley",
      OPL_TKE_NODEPOOL_AUTOSCALING_GROUP_PARA_JSON: JSON.stringify({ MinSize: 0, MaxSize: 1, DesiredCapacity: 1 }),
      OPL_TKE_NODEPOOL_LAUNCH_CONFIGURE_PARA_JSON: JSON.stringify({ InstanceType: "${INSTANCE_TYPE}" }),
      TENCENT_TCR_REGISTRY: "uswccr.ccs.tencentyun.com",
      TENCENT_TCR_NAMESPACE: "oplcloud",
      TENCENT_TCR_REGION: "na-siliconvalley",
      TENCENT_DEPLOY_KUBECONFIG_REF: "/var/run/opl-cloud/kubeconfig/kubeconfig"
    }
  });

  const text = JSON.stringify(rendered);
  assert.equal(text.includes("registry.example.com"), false);
  assert.equal(text.includes("cls-xxxxxxxx"), false);
  assert.equal(text.includes("postgresql://"), false);

  const items = rendered.items;
  const namespace = items.find((item) => item.kind === "Namespace");
  const config = items.find((item) => item.kind === "ConfigMap");
  const deployment = items.find((item) => item.kind === "Deployment");
  const ingress = items.find((item) => item.kind === "Ingress");
  const serviceConfig = items.find((item) => item.kind === "TkeServiceConfig");

  assert.equal(namespace.metadata.name, "opl-cloud");
  assert.equal(config.metadata.namespace, "opl-cloud");
  assert.equal(config.data.OPL_CLOUD_IMAGE, "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud:test");
  assert.equal(config.data.OPL_WORKSPACE_IMAGE, "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:latest");
  assert.equal(config.data.OPL_BILLING_MARKUP, env.OPL_BILLING_MARKUP);
  assert.equal(config.data.OPL_BASIC_COMPUTE_HOURLY_CNY, env.OPL_BASIC_COMPUTE_HOURLY_CNY);
  assert.equal(config.data.OPL_PRO_COMPUTE_HOURLY_CNY, env.OPL_PRO_COMPUTE_HOURLY_CNY);
  assert.equal(config.data.OPL_GPU_COMPUTE_HOURLY_CNY, undefined);
  assert.equal(config.data.OPL_STORAGE_GB_MONTH_CNY, env.OPL_STORAGE_GB_MONTH_CNY);
  assert.equal(config.data.OPL_CODEX_MODEL, "gpt-5.5");
  assert.equal(config.data.OPL_CODEX_REASONING_EFFORT, "xhigh");
  assert.equal(config.data.OPL_CODEX_BASE_URL, "https://gflabtoken.cn/v1");
  assert.equal(config.data.TENCENT_DEPLOY_CLUSTER_ID, "cls-oplcloud");
  assert.equal(config.data.TENCENT_TKE_REGION, "na-siliconvalley");
  assert.match(config.data.OPL_TKE_NODEPOOL_AUTOSCALING_GROUP_PARA_JSON, /DesiredCapacity/);
  assert.match(config.data.OPL_TKE_NODEPOOL_LAUNCH_CONFIGURE_PARA_JSON, /\$\{INSTANCE_TYPE\}/);
  assert.equal(config.data.TENCENT_TCR_REGISTRY, "uswccr.ccs.tencentyun.com");
  assert.equal(deployment.spec.template.spec.containers[0].image, "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud:test");
  assert.deepEqual(deployment.spec.template.spec.imagePullSecrets, [{ name: "tcr-pull-secret" }]);
  assert.equal(ingress.spec.ingressClassName, "qcloud");
  assert.equal(ingress.metadata.annotations["ingress.cloud.tencent.com/tke-service-config"], "opl-cloud-ingress-config");
  assert.deepEqual(ingress.spec.tls, [
    { hosts: ["cloud.medopl.cn"], secretName: "opl-cloud-console-medopl-cn-tls" },
    { hosts: ["workspace.medopl.cn"], secretName: "opl-cloud-workspace-medopl-cn-tls" }
  ]);
  assert.deepEqual(ingress.spec.rules.map((rule) => rule.host), ["cloud.medopl.cn", "workspace.medopl.cn"]);
  assert.equal(serviceConfig.metadata.namespace, "opl-cloud");
  assert.equal(serviceConfig.spec.loadBalancer.l7Listeners[0].domains[0].domain, "workspace.medopl.cn");
  assert.equal(serviceConfig.spec.loadBalancer.l7Listeners[0].domains[0].http2, false);
});

test("TKE manifest renderer can skip the shared Ingress during deploy so Workspace routes are not overwritten", async () => {
  const source = await readFile("deploy/tke/opl-cloud.k8s.json", "utf8");
  const manifest = JSON.parse(source);
  const { env } = await pricingContractValues();
  const rendered = renderTkeManifest({
    manifest,
    skipSharedIngress: true,
    values: {
      OPL_K8S_NAMESPACE: "opl-cloud",
      OPL_PUBLIC_URL: "https://cloud.medopl.cn",
      OPL_CONSOLE_DOMAIN: "cloud.medopl.cn",
      OPL_WORKSPACE_DOMAIN: "workspace.medopl.cn",
      OPL_CLOUD_IMAGE: "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud:test",
      OPL_WORKSPACE_IMAGE: "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:latest",
      OPL_IMAGE_PULL_SECRET_NAME: "tcr-pull-secret",
      OPL_WORKSPACE_STORAGE_CLASS: "cbs",
      OPL_BILLING_MARKUP: env.OPL_BILLING_MARKUP,
      OPL_BASIC_COMPUTE_HOURLY_CNY: env.OPL_BASIC_COMPUTE_HOURLY_CNY,
      OPL_PRO_COMPUTE_HOURLY_CNY: env.OPL_PRO_COMPUTE_HOURLY_CNY,
      OPL_STORAGE_GB_MONTH_CNY: env.OPL_STORAGE_GB_MONTH_CNY,
      OPL_CODEX_MODEL: "gpt-5.5",
      OPL_CODEX_REASONING_EFFORT: "xhigh",
      OPL_CODEX_BASE_URL: "https://gflabtoken.cn/v1",
      OPL_CONSOLE_TLS_SECRET_NAME: "opl-cloud-console-medopl-cn-tls",
      OPL_WORKSPACE_TLS_SECRET_NAME: "opl-cloud-workspace-medopl-cn-tls",
      OPL_INGRESS_CLASS: "qcloud",
      TENCENT_DEPLOY_CLUSTER_ID: "cls-oplcloud",
      TENCENT_TKE_REGION: "na-siliconvalley",
      OPL_TKE_NODEPOOL_AUTOSCALING_GROUP_PARA_JSON: JSON.stringify({ MinSize: 0, MaxSize: 1, DesiredCapacity: 1 }),
      OPL_TKE_NODEPOOL_LAUNCH_CONFIGURE_PARA_JSON: JSON.stringify({ InstanceType: "${INSTANCE_TYPE}" }),
      TENCENT_TCR_REGISTRY: "uswccr.ccs.tencentyun.com",
      TENCENT_TCR_NAMESPACE: "oplcloud",
      TENCENT_TCR_REGION: "na-siliconvalley",
      TENCENT_DEPLOY_KUBECONFIG_REF: "/var/run/opl-cloud/kubeconfig/kubeconfig"
    }
  });

  assert.equal(rendered.items.some((item) => item.kind === "Ingress" && item.metadata.name === "opl-cloud"), false);
  assert.equal(rendered.items.some((item) => item.kind === "TkeServiceConfig" && item.metadata.name === "opl-cloud-ingress-config"), true);
});

test("TKE production deploy patches shared Ingress config without overwriting Workspace routes", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const currentJob = job(workflow, "deploy");
  const step = stepsByName(currentJob).get("Render and apply manifest");
  const text = serializedStep(step);

  assert.match(text, /apply -f "\$OPL_DEPLOY_SECRET_DIR\/opl-cloud\.rendered\.json"/);
  assert.match(text, /annotate ingress opl-cloud ingress\.cloud\.tencent\.com\/tke-service-config=opl-cloud-ingress-config --overwrite/);
  assert.doesNotMatch(text, /kubectl .*apply -f "\$OPL_DEPLOY_SECRET_DIR\/opl-cloud\.ingress-bootstrap\.json"[\s\S]*kubectl .*apply -f "\$OPL_DEPLOY_SECRET_DIR\/opl-cloud\.ingress-bootstrap\.json"/);
});

test("TKE production diagnostics workflow is read-only and matches the deployment contract", async () => {
  const contract = await readJson(deploymentContractPath);
  const workflow = await readWorkflow(contract.diagnosticsWorkflow.file);
  assertWorkflowContract(workflow, contract.diagnosticsWorkflow, contract);
});

test("TKE production E2E resolves operator token from the existing cluster secret when GitHub secret is absent", async () => {
  const workflow = await readWorkflow(".github/workflows/e2e-tke-production.yml");
  const currentJob = job(workflow, "persistence-e2e");
  const stepMap = stepsByName(currentJob);
  const step = stepMap.get("Resolve operator token");

  assert.ok(step, "E2E workflow must resolve the operator token before running API mutations");
  const text = serializedStep(step);
  assert.match(text, /OPL_VERIFY_OPERATOR_TOKEN:-/);
  assert.match(text, /get secret opl-cloud-operator/);
  assert.match(text, /OPL_OPERATOR_SUMMARY_TOKEN/);
  assert.match(text, /::add-mask::/);
  assert.match(text, /OPL_VERIFY_OPERATOR_TOKEN<<EOF/);
  assert.ok(
    [...stepMap.keys()].indexOf("Resolve operator token") < [...stepMap.keys()].indexOf("Run production persistence E2E"),
    "operator token must be resolved before running the production E2E"
  );
});

test("TKE production E2E resolves admin credentials from auth seed when operator token is absent", async () => {
  const workflow = await readWorkflow(".github/workflows/e2e-tke-production.yml");
  const currentJob = job(workflow, "persistence-e2e");
  const stepMap = stepsByName(currentJob);
  const step = stepMap.get("Resolve Console admin credentials");

  assert.ok(step, "E2E workflow must resolve admin credentials before running API mutations");
  const text = serializedStep(step);
  assert.match(text, /get secret opl-cloud-auth/);
  assert.match(text, /OPL_CONSOLE_USERS_JSON/);
  assert.match(text, /role === "admin"/);
  assert.match(text, /admin\.password/);
  assert.match(text, /::add-mask::/);
  assert.match(text, /OPL_VERIFY_ADMIN_EMAIL=/);
  assert.match(text, /OPL_VERIFY_ADMIN_PASSWORD<<EOF/);
  assert.ok(
    [...stepMap.keys()].indexOf("Resolve Console admin credentials") < [...stepMap.keys()].indexOf("Run production persistence E2E"),
    "admin credentials must be resolved before running the production E2E"
  );
});

test("TKE diagnostics do not print account state or Workspace URL tokens", async () => {
  const contract = await readJson(deploymentContractPath);
  const workflow = await readWorkflow(contract.diagnosticsWorkflow.file);
  const currentJob = job(workflow, contract.diagnosticsWorkflow.job);
  const step = stepsByName(currentJob).get("Check control plane API through port-forward");
  const text = serializedStep(step);

  assert.match(text, /\/api\/state/, "diagnostics may check state reachability");
  assert.match(text, /\/api\/production\/readiness/, "diagnostics must still check production readiness");
  assert.doesNotMatch(text, /cat \/tmp\/opl-cloud-state\.json/, "diagnostics must not print /api/state payloads");
  assert.doesNotMatch(text, /printf 'state='/, "diagnostics must not label and dump account state");
});
