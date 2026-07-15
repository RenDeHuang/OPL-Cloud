const PROVIDERS = {
  TENCENT_TKE: "tencent-tke"
};

const REQUIRED_COMMON_ENV = [
  "OPL_RUNTIME_PROVIDER",
  "DATABASE_URL",
  "OPL_INTERNAL_SERVICE_TOKEN",
  "OPL_CONSOLE_USERS_JSON",
  "OPL_WORKSPACE_DOMAIN",
  "OPL_WORKSPACE_IMAGE"
];

const REQUIRED_TKE_ENV = [
  "OPL_PUBLIC_URL",
  "OPL_CONSOLE_DOMAIN",
  "OPL_CLOUD_IMAGE",
  "OPL_K8S_NAMESPACE",
  "OPL_INGRESS_CLASS",
  "OPL_IMAGE_PULL_SECRET_NAME",
  "OPL_WORKSPACE_STORAGE_CLASS",
  "OPL_TENCENT_ZONE",
  "TENCENT_DEPLOY_KUBECONFIG_REF",
  "TENCENT_DEPLOY_CLUSTER_ID",
  "TENCENT_TCR_REGISTRY",
  "TENCENT_TCR_NAMESPACE",
  "TENCENT_TCR_REGION"
];

const SECRET_COMMON_ENV = [
  "DATABASE_URL",
  "OPL_INTERNAL_SERVICE_TOKEN",
  "OPL_CONSOLE_USERS_JSON"
];

const SECRET_TKE_ENV = [
  "TENCENT_DEPLOY_KUBECONFIG_REF"
];

const PROVIDER_CONFIG = {
  [PROVIDERS.TENCENT_TKE]: {
    requiredEnv: REQUIRED_TKE_ENV,
    secretEnv: SECRET_TKE_ENV
  }
};

function check(id, ok, message) {
  return { id, ok, message };
}

function valueOf(entry) {
  if (entry && typeof entry === "object" && "value" in entry) return entry.value;
  if (typeof entry === "string") return entry;
  return "";
}

function hasSecretRef(entry) {
  return Boolean(entry && typeof entry === "object" && entry.secretRef);
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

export function productionManifestRequiredEnv() {
  return [...new Set([
    ...REQUIRED_COMMON_ENV,
    ...REQUIRED_TKE_ENV
  ])];
}

export function validateProductionManifest({ env = {} } = {}) {
  const values = Object.fromEntries(Object.entries(env).map(([key, entry]) => [key, valueOf(entry)]));
  const provider = values.OPL_RUNTIME_PROVIDER || "";
  const providerConfig = PROVIDER_CONFIG[provider] || { requiredEnv: [], secretEnv: [] };
  const requiredEnv = [
    ...REQUIRED_COMMON_ENV,
    ...providerConfig.requiredEnv
  ];
  const secretEnv = [
    ...SECRET_COMMON_ENV,
    ...providerConfig.secretEnv
  ];
  const missingEnv = requiredEnv.filter((key) => !env[key] || (!hasSecretRef(env[key]) && !String(valueOf(env[key])).trim()));
  const inlineSecretEnv = secretEnv.filter((key) => env[key] && !hasSecretRef(env[key]));
  const checks = [
    check("required_env", missingEnv.length === 0, "Every production launch variable must be declared"),
    check("secret_refs", inlineSecretEnv.length === 0, "Sensitive production values must use secretRef"),
    check("runtime_provider", provider === PROVIDERS.TENCENT_TKE, "OPL_RUNTIME_PROVIDER must be tencent-tke"),
    check(
      "registry_images",
      looksLikeRegistryImage({ image: values.OPL_CLOUD_IMAGE, registry: values.TENCENT_TCR_REGISTRY }) &&
        looksLikeRegistryImage({ image: values.OPL_WORKSPACE_IMAGE, registry: values.TENCENT_TCR_REGISTRY }),
      "OPL_CLOUD_IMAGE and OPL_WORKSPACE_IMAGE must use TCR repository@sha256 references"
    ),
    check("workspace_domain", looksLikeProductionDomain(values.OPL_WORKSPACE_DOMAIN), "OPL_WORKSPACE_DOMAIN must be a production wildcard domain")
  ];
  const failedChecks = checks.filter((item) => !item.ok).map((item) => item.id);

  return {
    ok: missingEnv.length === 0 && inlineSecretEnv.length === 0 && failedChecks.length === 0,
    missingEnv,
    inlineSecretEnv,
    failedChecks,
    checks
  };
}
