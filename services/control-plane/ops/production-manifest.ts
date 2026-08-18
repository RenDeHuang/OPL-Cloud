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
  "OPL_SYSTEM_COMPUTE_MACHINE_TYPE",
  "OPL_SYSTEM_COMPUTE_CVM_ID",
  "OPL_FABRIC_TENCENT_TKE_PROVIDER_PROFILE_JSON",
  "TENCENT_CBS_DISK_TYPE",
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
  const machineType = String(values.OPL_SYSTEM_COMPUTE_MACHINE_TYPE || "").trim();
  const cvmId = String(values.OPL_SYSTEM_COMPUTE_CVM_ID || "").trim();
  const cvmIdentityValid = machineType === "NativeCVM"
    ? /^ins-[A-Za-z0-9]+$/.test(cvmId)
    : (machineType === "Native" || machineType === "CXM") && cvmId === "";
  return /^np-[A-Za-z0-9-]+$/.test(systemPool) &&
    Boolean(String(values.OPL_SYSTEM_COMPUTE_MACHINE_ID || "").trim()) &&
    Boolean(String(values.OPL_SYSTEM_COMPUTE_NODE_NAME || "").trim()) &&
    cvmIdentityValid;
}

function hasValidTencentProviderProfile(raw, systemPoolID) {
  let profile;
  try {
    profile = JSON.parse(String(raw || ""));
  } catch {
    return false;
  }
  if (!profile || profile.schemaVersion !== 1 || !Array.isArray(profile.packages) || profile.packages.length === 0) return false;
  const packageIDs = new Set();
  const nodePoolIDs = new Set();
  let availableCount = 0;
  for (const item of profile.packages) {
    const compute = item?.compute || {};
    const storage = item?.storage || {};
    const billing = item?.billing || {};
    const packageID = String(item?.id || "").trim();
    const nodePoolID = String(item?.nodePoolId || "").trim();
    const sizeGB = Number(storage.sizeGb);
    if (!packageID || packageIDs.has(packageID) || !String(item?.name || "").trim() ||
      !String(compute.id || "").trim() || !String(compute.server || "").trim() ||
      !String(compute.instanceType || "").trim() || Number(compute.cpu) <= 0 || Number(compute.memoryGb) <= 0 ||
      Number(compute.diskGb) !== sizeGB || !/^np-[A-Za-z0-9-]+$/.test(nodePoolID) || nodePoolID === systemPoolID ||
      nodePoolIDs.has(nodePoolID) || !Number.isInteger(Number(item.maxReplicas)) || Number(item.maxReplicas) <= 0 ||
      !String(item.zone || "").trim() || !Number.isInteger(sizeGB) || sizeGB < 10 || sizeGB % 10 !== 0 ||
      !String(storage.diskType || "").trim() || billing.chargeType !== "PREPAID" || billing.periodMonths !== 1 ||
      billing.renewFlag !== "NOTIFY_AND_MANUAL_RENEW") return false;
    packageIDs.add(packageID);
    nodePoolIDs.add(nodePoolID);
    if (item.available === true) availableCount += 1;
  }
  return availableCount > 0;
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
  const systemMachineType = String(values.OPL_SYSTEM_COMPUTE_MACHINE_TYPE || "").trim();
  const missingEnv = requiredEnv.filter((key) => {
    if (key === "OPL_SYSTEM_COMPUTE_CVM_ID" && (systemMachineType === "Native" || systemMachineType === "CXM")) {
      return !Object.hasOwn(env, key);
    }
    return !env[key] || (!hasSecretRef(env[key]) && !String(valueOf(env[key])).trim());
  });
  const inlineSecretEnv = secretEnv.filter((key) => env[key] && !hasSecretRef(env[key]));
  const hasVerificationMutationAuthority = FORBIDDEN_VERIFICATION_MUTATION_ENV.some((key) => Object.hasOwn(env, key));
  const checks = [
    check("required_env", missingEnv.length === 0, "Every production launch variable must be declared"),
    check("secret_refs", inlineSecretEnv.length === 0, "Sensitive production values must use secretRef"),
    check("runtime_provider", provider === PROVIDERS.TENCENT_TKE, "OPL_RUNTIME_PROVIDER must be tencent-tke"),
    check("tencent_provider_profile", hasValidTencentProviderProfile(values.OPL_FABRIC_TENCENT_TKE_PROVIDER_PROFILE_JSON, values.OPL_SYSTEM_COMPUTE_NODE_POOL_ID), "Tencent Provider Profile must contain executable package and NodePool bindings"),
    check("verification_mutation_authority", !hasVerificationMutationAuthority, "Ordinary production manifests must not carry real-verification approvals or write flags"),
    check("system_compute_identity", hasDedicatedNodePoolIdentity(values), "System compute identity must be explicit and valid"),
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
