const PROVIDERS = {
  TENCENT_TKE: "tencent-tke"
};
const REQUIRED_COMMON_ENV = [
  "OPL_RUNTIME_PROVIDER",
  "DATABASE_URL",
  "OPL_INTERNAL_SERVICE_TOKEN",
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
  "OPL_SYSTEM_COMPUTE_NODE_POOL_ID",
  "OPL_SYSTEM_COMPUTE_MACHINE_ID",
  "OPL_SYSTEM_COMPUTE_NODE_NAME",
  "OPL_SYSTEM_COMPUTE_CVM_ID",
  "OPL_BASIC_COMPUTE_NODE_POOL_ID",
  "OPL_PRO_COMPUTE_NODE_POOL_ID",
  "OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS",
  "OPL_PRO_COMPUTE_NODE_POOL_MAX_REPLICAS",
  "TENCENT_DEPLOY_KUBECONFIG_REF",
  "TENCENT_DEPLOY_CLUSTER_ID",
  "TENCENT_TCR_REGISTRY",
  "TENCENT_TCR_NAMESPACE",
  "TENCENT_TCR_REGION"
];

const SECRET_COMMON_ENV = [
  "DATABASE_URL",
  "OPL_INTERNAL_SERVICE_TOKEN"
];

const SECRET_TKE_ENV = [
  "TENCENT_DEPLOY_KUBECONFIG_REF"
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

function hasDedicatedNodePoolIdentity(values) {
  const systemPool = String(values.OPL_SYSTEM_COMPUTE_NODE_POOL_ID || "").trim();
  const basicPool = String(values.OPL_BASIC_COMPUTE_NODE_POOL_ID || "").trim();
  const proPool = String(values.OPL_PRO_COMPUTE_NODE_POOL_ID || "").trim();
  const pools = [systemPool, basicPool, proPool];
  return pools.every((value) => /^np-[A-Za-z0-9-]+$/.test(value)) &&
    new Set(pools).size === pools.length &&
    Boolean(String(values.OPL_SYSTEM_COMPUTE_MACHINE_ID || "").trim()) &&
    Boolean(String(values.OPL_SYSTEM_COMPUTE_NODE_NAME || "").trim()) &&
    /^ins-[A-Za-z0-9]+$/.test(String(values.OPL_SYSTEM_COMPUTE_CVM_ID || "").trim());
}

function isPositiveInt64(value) {
  const normalized = String(value || "").trim();
  if (!/^[1-9][0-9]*$/.test(normalized)) return false;
  try {
    return BigInt(normalized) <= 9223372036854775807n;
  } catch {
    return false;
  }
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
  const hasVerificationMutationAuthority = FORBIDDEN_VERIFICATION_MUTATION_ENV.some((key) => Object.hasOwn(env, key));
  const checks = [
    check("required_env", missingEnv.length === 0, "Every production launch variable must be declared"),
    check("secret_refs", inlineSecretEnv.length === 0, "Sensitive production values must use secretRef"),
    check("runtime_provider", provider === PROVIDERS.TENCENT_TKE, "OPL_RUNTIME_PROVIDER must be tencent-tke"),
    check("verification_mutation_authority", !hasVerificationMutationAuthority, "Ordinary production manifests must not carry real-verification approvals or write flags"),
    check("dedicated_node_pool_identity", hasDedicatedNodePoolIdentity(values), "System, Basic, and Pro resource identities must be explicit, valid, and distinct"),
    check(
      "dedicated_node_pool_capacity",
      isPositiveInt64(values.OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS) && isPositiveInt64(values.OPL_PRO_COMPUTE_NODE_POOL_MAX_REPLICAS),
      "Basic and Pro NodePool maxReplicas must be explicitly approved positive int64 values"
    ),
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
