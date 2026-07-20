import { access } from "node:fs/promises";
import { constants } from "node:fs";
import { delimiter, join } from "node:path";

const PROVIDERS = {
  TENCENT_TKE: "tencent-tke"
};

const REQUIRED_COMMON_ENV = [
  "OPL_RUNTIME_PROVIDER",
  "OPL_WORKSPACE_IMAGE",
  "OPL_WORKSPACE_DOMAIN",
  "OPL_AIONUI_ADMIN_PASSWORD_SEED",
  "DATABASE_URL"
];

const REQUIRED_TKE_ENV = [
  "OPL_CLOUD_IMAGE",
  "OPL_K8S_NAMESPACE",
  "OPL_INGRESS_CLASS",
  "OPL_IMAGE_PULL_SECRET_NAME",
  "OPL_WORKSPACE_STORAGE_CLASS",
  "OPL_TENCENT_PROVISIONER_BIN",
  "OPL_TENCENT_ZONE",
  "TENCENTCLOUD_SECRET_ID",
  "TENCENTCLOUD_SECRET_KEY",
  "TENCENTCLOUD_REGION",
  "TENCENT_DEPLOY_KUBECONFIG_REF",
  "TENCENT_DEPLOY_CLUSTER_ID",
  "TENCENT_CVM_SUBNET_ID",
  "TENCENT_CVM_SECURITY_GROUP_IDS",
  "RUN_TENCENT_CREATE_RELEASE_EXECUTION",
  "TENCENT_TCR_REGISTRY",
  "TENCENT_TCR_NAMESPACE",
  "TENCENT_TCR_REGION"
];

const FORBIDDEN_VERIFICATION_MUTATION_ENV = [
  "OPL_VERIFY_MUTATION_APPROVAL_JSON",
  "OPL_VERIFY_MUTATION_APPROVAL_ID",
  "OPL_VERIFY_ALLOW_GATEWAY_WRITE",
  "OPL_VERIFY_ALLOW_MODEL_WRITE",
  "OPL_VERIFY_ALLOW_PROVIDER_WRITE"
];

const PROVIDER_CONFIG = {
  [PROVIDERS.TENCENT_TKE]: {
    requiredEnv: REQUIRED_TKE_ENV,
    requiredTools: ["kubectl", "env:OPL_TENCENT_PROVISIONER_BIN"]
  }
};

function check(id, ok, message) {
  return { id, ok, message };
}

function normalizeRegistry(registry) {
  return String(registry || "").replace(/^https?:\/\//, "").replace(/\/$/, "");
}

function looksLikeRegistryImage({ image, registry }) {
  const normalizedRegistry = normalizeRegistry(registry);
  const match = String(image || "").match(/^([^@]+)@sha256:[0-9a-f]{64}$/);
  const repository = match?.[1] || "";
  return Boolean(
    normalizedRegistry &&
    repository.startsWith(`${normalizedRegistry}/`) &&
    !repository.slice(repository.lastIndexOf("/") + 1).includes(":")
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

function hasAionUiAdminPasswordSeed(env) {
  const seed = String(env.OPL_AIONUI_ADMIN_PASSWORD_SEED || "");
  return seed.length >= 24 && !/(password|changeme|change-me|example|placeholder|seed)/i.test(seed);
}

async function commandExistsInPath(command, env) {
  if (command.includes("/")) {
    try {
      await access(command, constants.X_OK);
      return true;
    } catch {
      return false;
    }
  }

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
  const hasEnv = (key) => Boolean(String(env[key] ?? "").trim());

  const requiredEnv = [
    ...REQUIRED_COMMON_ENV,
    ...providerConfig.requiredEnv
  ];
  for (const key of requiredEnv) {
    if (!hasEnv(key)) missingEnv.push(key);
  }
  for (const tool of providerConfig.requiredTools) {
    const command = tool.startsWith("env:") ? env[tool.slice(4)] : tool;
    if (!command || !(await commandExists(command))) missingTools.push(command || tool);
  }
  const providerEnvReady = providerConfig.requiredEnv.every(hasEnv) && (
    provider !== PROVIDERS.TENCENT_TKE ||
    String(env.OPL_TENCENT_ZONE).replace(/-\d+$/, "") === String(env.TENCENTCLOUD_REGION)
  );

  const checks = [
    check("runtime_provider", provider === PROVIDERS.TENCENT_TKE, "OPL_RUNTIME_PROVIDER must be tencent-tke"),
    check(
      "registry_images",
      looksLikeRegistryImage({ image: env.OPL_CLOUD_IMAGE, registry: env.TENCENT_TCR_REGISTRY }) &&
        looksLikeRegistryImage({ image: env.OPL_WORKSPACE_IMAGE, registry: env.TENCENT_TCR_REGISTRY }),
      "OPL_CLOUD_IMAGE and OPL_WORKSPACE_IMAGE must use configured TCR repository@sha256 references"
    ),
    check("opl_app_contract", matchesOplAppContract(env), "one-person-lab-app WebUI must expose port 3000 and persist /data plus /projects"),
    check("aionui_admin_password_seed", hasAionUiAdminPasswordSeed(env), "AionUI WebUI login must use a strong per-Workspace password seed"),
    check("workspace_domain", looksLikeProductionDomain(env.OPL_WORKSPACE_DOMAIN), "OPL_WORKSPACE_DOMAIN must be a production wildcard domain"),
    check("database_url", Boolean(env.DATABASE_URL), "DATABASE_URL is required for production persistence"),
    check("provider_env", providerEnvReady, "Runtime provider environment is incomplete or its Tencent zone and region do not match"),
    check(
      "live_mutation_guard",
      env.RUN_TENCENT_CREATE_RELEASE_EXECUTION === "1" && FORBIDDEN_VERIFICATION_MUTATION_ENV.every((key) => !hasEnv(key)),
      "Production compute allocation must be enabled without deploying real-verification approval or write authority"
    ),
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
