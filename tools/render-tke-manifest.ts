import { readFile, writeFile } from "node:fs/promises";

const DEPLOY_VALUE_KEYS = [
  "OPL_K8S_NAMESPACE",
  "OPL_PUBLIC_URL",
  "OPL_CONSOLE_DOMAIN",
  "OPL_WORKSPACE_DOMAIN",
  "OPL_CLOUD_IMAGE",
  "OPL_WORKSPACE_IMAGE",
  "OPL_IMAGE_PULL_SECRET_NAME",
  "OPL_WORKSPACE_STORAGE_CLASS",
  "OPL_TENCENT_PROVISIONER_BIN",
  "OPL_BASIC_COMPUTE_HOURLY_CNY",
  "OPL_PRO_COMPUTE_HOURLY_CNY",
  "OPL_STORAGE_GB_MONTH_CNY",
  "OPL_RESOURCE_BILLING_WORKER_ENABLED",
  "OPL_RESOURCE_BILLING_INTERVAL_MS",
  "OPL_BASIC_COMPUTE_INSTANCE_TYPE",
  "OPL_PRO_COMPUTE_INSTANCE_TYPE",
  "OPL_CODEX_MODEL",
  "OPL_CODEX_REASONING_EFFORT",
  "OPL_CODEX_BASE_URL",
  "OPL_CONSOLE_TLS_SECRET_NAME",
  "OPL_WORKSPACE_TLS_SECRET_NAME",
  "OPL_INGRESS_CLASS",
  "TENCENTCLOUD_REGION",
  "TENCENT_DEPLOY_CLUSTER_ID",
  "TENCENT_CVM_SUBNET_ID",
  "TENCENT_CVM_SECURITY_GROUP_IDS",
  "TENCENT_CVM_SYSTEM_DISK_TYPE",
  "TENCENT_CVM_SYSTEM_DISK_SIZE_GB",
  "RUN_TENCENT_CREATE_RELEASE_EXECUTION",
  "TENCENT_TCR_REGISTRY",
  "TENCENT_TCR_NAMESPACE",
  "TENCENT_TCR_REGION",
  "TENCENT_DEPLOY_KUBECONFIG_REF"
];
const OPTIONAL_DEPLOY_VALUE_KEYS = [
  "OPL_WORKSPACE_VOLUME_SNAPSHOT_CLASS",
  "OPL_BASIC_COMPUTE_NODE_POOL_ID",
  "OPL_PRO_COMPUTE_NODE_POOL_ID"
];

function requiredValues(values) {
  const missing = DEPLOY_VALUE_KEYS.filter((key) => !values[key]);
  if (missing.length) throw new Error(`missing_tke_manifest_values:${missing.join(",")}`);
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function setNamespace(item, namespace) {
  item.metadata = item.metadata || {};
  if (item.kind === "Namespace") {
    item.metadata.name = namespace;
  } else {
    item.metadata.namespace = namespace;
  }
}

function setConfigMap(item, values) {
  if (item.kind !== "ConfigMap" || !item.data) return;
  for (const key of [...DEPLOY_VALUE_KEYS, ...OPTIONAL_DEPLOY_VALUE_KEYS]) {
    if (Object.prototype.hasOwnProperty.call(item.data, key)) {
      item.data[key] = values[key] ? String(values[key]) : "";
    }
  }
}

function setDeployment(item, values) {
  if (item.kind !== "Deployment") return;
  const podSpec = item.spec?.template?.spec;
  if (!podSpec) return;

	podSpec.imagePullSecrets = [{ name: values.OPL_IMAGE_PULL_SECRET_NAME }];
	for (const container of [...(podSpec.initContainers || []), ...(podSpec.containers || [])]) {
		if (["control-plane", "ledger", "fabric", "ledger-schema-migration"].includes(container.name)) {
			container.image = values.OPL_CLOUD_IMAGE;
		}
	}
}

function setIngress(item, values) {
  if (item.kind !== "Ingress") return;
  item.spec = item.spec || {};
  item.spec.ingressClassName = values.OPL_INGRESS_CLASS;
  item.spec.tls = [
    {
      hosts: [values.OPL_CONSOLE_DOMAIN],
      secretName: values.OPL_CONSOLE_TLS_SECRET_NAME
    },
    {
      hosts: [values.OPL_WORKSPACE_DOMAIN],
      secretName: values.OPL_WORKSPACE_TLS_SECRET_NAME
    }
  ];

  const rules = item.spec.rules || [];
  if (rules[0]) rules[0].host = values.OPL_CONSOLE_DOMAIN;
  if (rules[1]) rules[1].host = values.OPL_WORKSPACE_DOMAIN;
}

export function renderTkeManifest({ manifest, values, skipSharedIngress = false } = {}) {
  requiredValues(values);
  const rendered = clone(manifest);
  if (skipSharedIngress) {
    rendered.items = (rendered.items || []).filter((item) => !(item.kind === "Ingress" && item.metadata?.name === "opl-cloud"));
  }
  for (const item of rendered.items || []) {
    setNamespace(item, values.OPL_K8S_NAMESPACE);
    setConfigMap(item, values);
    setDeployment(item, values);
    setIngress(item, values);
  }
  return rendered;
}

function cliArgs(argv) {
  const args = {};
  for (let index = 0; index < argv.length; index += 1) {
    const item = argv[index];
    if (!item.startsWith("--")) continue;
    const key = item.slice(2);
    const value = argv[index + 1] && !argv[index + 1].startsWith("--") ? argv[++index] : "true";
    args[key] = value;
  }
  return args;
}

export async function runRenderTkeManifestCli({
  argv = process.argv.slice(2),
  env = process.env,
  stdout = process.stdout
} = {}) {
  const args = cliArgs(argv);
  if (!args.manifest) throw new Error("manifest_path_required");
  const outputPath = args.out;
  const manifest = JSON.parse(await readFile(args.manifest, "utf8"));
  const values = Object.fromEntries([...DEPLOY_VALUE_KEYS, ...OPTIONAL_DEPLOY_VALUE_KEYS].map((key) => [key, env[key]]));
  const rendered = renderTkeManifest({
    manifest,
    values,
    skipSharedIngress: args["skip-shared-ingress"] === "true"
  });
  const output = `${JSON.stringify(rendered, null, 2)}\n`;
  if (outputPath) {
    await writeFile(outputPath, output, { mode: 0o600 });
  } else {
    stdout.write(output);
  }
  return 0;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  runRenderTkeManifestCli().then((code) => {
    process.exitCode = code;
  }).catch((error) => {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  });
}
