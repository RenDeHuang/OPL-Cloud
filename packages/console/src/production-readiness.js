import { access } from "node:fs/promises";
import { constants } from "node:fs";
import { delimiter, join } from "node:path";

const PROVIDERS = {
  TENCENT_CVM: "tencent-cvm",
  TENCENT_TKE: "tencent-tke"
};

const REQUIRED_COMMON_ENV = [
  "OPL_RUNTIME_PROVIDER",
  "OPL_WORKSPACE_IMAGE",
  "OPL_WORKSPACE_DOMAIN",
  "DATABASE_URL"
];

const REQUIRED_CVM_ENV = [
  "OPL_HARBOR_REGISTRY",
  "TENCENTCLOUD_SECRET_ID",
  "TENCENTCLOUD_SECRET_KEY",
  "TENCENTCLOUD_REGION",
  "OPL_VPC_ID",
  "OPL_SUBNET_ID",
  "OPL_SECURITY_GROUP_ID",
  "OPL_AVAILABILITY_ZONE",
  "OPL_IMAGE_ID",
  "OPL_SSH_KEY_ID"
];

const REQUIRED_TKE_ENV = [
  "OPL_CLOUD_IMAGE",
  "OPL_K8S_NAMESPACE",
  "OPL_INGRESS_CLASS",
  "OPL_IMAGE_PULL_SECRET_NAME",
  "OPL_WORKSPACE_STORAGE_CLASS",
  "TENCENT_DEPLOY_KUBECONFIG_REF",
  "TENCENT_DEPLOY_CLUSTER_ID",
  "TENCENT_TCR_REGISTRY",
  "TENCENT_TCR_NAMESPACE",
  "TENCENT_TCR_REGION"
];

const PROVIDER_CONFIG = {
  [PROVIDERS.TENCENT_CVM]: {
    requiredEnv: REQUIRED_CVM_ENV,
    requiredTools: ["tofu", "ansible-playbook", "tccli", "caddy"]
  },
  [PROVIDERS.TENCENT_TKE]: {
    requiredEnv: REQUIRED_TKE_ENV,
    requiredTools: ["kubectl"]
  }
};

function check(id, ok, message) {
  return { id, ok, message };
}

function normalizeRegistry(registry) {
  return String(registry || "").replace(/^https?:\/\//, "").replace(/\/$/, "");
}

function looksLikeHarborImage({ image, registry }) {
  const normalizedRegistry = normalizeRegistry(registry);
  return Boolean(
    image &&
    normalizedRegistry &&
    image.startsWith(`${normalizedRegistry}/`) &&
    image.includes("/") &&
    image.includes(":")
  );
}

function looksLikeRegistryImage({ image, registry }) {
  const normalizedRegistry = normalizeRegistry(registry);
  return Boolean(
    image &&
    normalizedRegistry &&
    image.startsWith(`${normalizedRegistry}/`) &&
    image.includes("/") &&
    image.includes(":")
  );
}

function looksLikeProductionDomain(domain) {
  return Boolean(domain && domain.includes(".") && !domain.includes("localhost") && !domain.startsWith("127."));
}

function matchesOplAppContract(env) {
  return (
    String(env.OPL_WORKSPACE_WEBUI_PORT || "3000") === "3000" &&
    String(env.OPL_WORKSPACE_DATA_DIR || "/data") === "/data" &&
    String(env.OPL_WORKSPACE_PROJECTS_DIR || "/projects") === "/projects"
  );
}

async function commandExistsInPath(command, env) {
  const pathValue = env.PATH || process.env.PATH || "";
  for (const dir of pathValue.split(delimiter).filter(Boolean)) {
    try {
      await access(join(dir, command), constants.X_OK);
      return true;
    } catch {
      // Keep scanning PATH.
    }
  }
  return false;
}

export async function productionReadiness({ env = process.env, commandExists = (command) => commandExistsInPath(command, env) } = {}) {
  const missingEnv = [];
  const missingTools = [];
  const provider = env.OPL_RUNTIME_PROVIDER || "";
  const providerConfig = PROVIDER_CONFIG[provider] || { requiredEnv: [], requiredTools: [] };

  const requiredEnv = [
    ...REQUIRED_COMMON_ENV,
    ...providerConfig.requiredEnv
  ];
  for (const key of requiredEnv) {
    if (!env[key]) missingEnv.push(key);
  }
  for (const tool of providerConfig.requiredTools) {
    if (!(await commandExists(tool))) missingTools.push(tool);
  }

  const checks = [
    check("runtime_provider", Boolean(PROVIDER_CONFIG[provider]), "OPL_RUNTIME_PROVIDER must be tencent-cvm or tencent-tke"),
    provider === PROVIDERS.TENCENT_TKE
      ? check(
        "registry_images",
        looksLikeRegistryImage({ image: env.OPL_CLOUD_IMAGE, registry: env.TENCENT_TCR_REGISTRY }) &&
          looksLikeRegistryImage({ image: env.OPL_WORKSPACE_IMAGE, registry: env.TENCENT_TCR_REGISTRY }),
        "OPL_CLOUD_IMAGE and OPL_WORKSPACE_IMAGE must point to the configured TCR registry"
      )
      : check(
        "harbor_image",
        looksLikeHarborImage({ image: env.OPL_WORKSPACE_IMAGE, registry: env.OPL_HARBOR_REGISTRY }),
        "OPL_WORKSPACE_IMAGE must point to the configured Harbor production registry"
      ),
    check("opl_app_contract", matchesOplAppContract(env), "one-person-lab-app WebUI must expose port 3000 and persist /data plus /projects"),
    check("workspace_domain", looksLikeProductionDomain(env.OPL_WORKSPACE_DOMAIN), "OPL_WORKSPACE_DOMAIN must be a production wildcard domain"),
    check("database_url", Boolean(env.DATABASE_URL), "DATABASE_URL is required for production persistence"),
    check("provider_env", providerConfig.requiredEnv.every((key) => Boolean(env[key])), "Runtime provider environment is incomplete"),
    check("tools", missingTools.length === 0, "Required production tools are missing")
  ];
  const failedChecks = checks.filter((item) => !item.ok).map((item) => item.id);

  return {
    ready: missingEnv.length === 0 && missingTools.length === 0 && failedChecks.length === 0,
    missingEnv,
    missingTools,
    failedChecks,
    checks
  };
}
