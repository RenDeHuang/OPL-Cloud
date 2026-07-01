const REQUIRED_ENV = [
  "OPL_RUNTIME_PROVIDER",
  "DATABASE_URL",
  "OPENMETER_ENDPOINT",
  "OPENMETER_API_KEY",
  "TENCENTCLOUD_SECRET_ID",
  "TENCENTCLOUD_SECRET_KEY",
  "TENCENTCLOUD_REGION",
  "OPL_HARBOR_REGISTRY",
  "OPL_WORKSPACE_DOMAIN",
  "OPL_WORKSPACE_IMAGE",
  "OPL_VPC_ID",
  "OPL_SUBNET_ID",
  "OPL_SECURITY_GROUP_ID",
  "OPL_AVAILABILITY_ZONE",
  "OPL_IMAGE_ID",
  "OPL_SSH_KEY_ID"
];

const SECRET_ENV = [
  "DATABASE_URL",
  "OPENMETER_ENDPOINT",
  "OPENMETER_API_KEY",
  "TENCENTCLOUD_SECRET_ID",
  "TENCENTCLOUD_SECRET_KEY",
  "OPL_VPC_ID",
  "OPL_SUBNET_ID",
  "OPL_SECURITY_GROUP_ID",
  "OPL_IMAGE_ID",
  "OPL_SSH_KEY_ID"
];

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

function looksLikeHarborImage({ image, registry }) {
  const normalizedRegistry = normalizeRegistry(registry);
  return Boolean(image && normalizedRegistry && image.startsWith(`${normalizedRegistry}/`) && image.includes(":"));
}

function looksLikeProductionDomain(domain) {
  return Boolean(domain && domain.includes(".") && !domain.includes("localhost") && !domain.startsWith("127."));
}

export function productionManifestRequiredEnv() {
  return [...REQUIRED_ENV];
}

export function validateProductionManifest({ env = {} } = {}) {
  const missingEnv = REQUIRED_ENV.filter((key) => !env[key]);
  const inlineSecretEnv = SECRET_ENV.filter((key) => env[key] && !hasSecretRef(env[key]));
  const values = Object.fromEntries(Object.entries(env).map(([key, entry]) => [key, valueOf(entry)]));
  const checks = [
    check("required_env", missingEnv.length === 0, "Every production launch variable must be declared"),
    check("secret_refs", inlineSecretEnv.length === 0, "Sensitive production values must use secretRef"),
    check("runtime_provider", values.OPL_RUNTIME_PROVIDER === "tencent-cvm", "OPL_RUNTIME_PROVIDER must be tencent-cvm"),
    check(
      "harbor_image",
      looksLikeHarborImage({ image: values.OPL_WORKSPACE_IMAGE, registry: values.OPL_HARBOR_REGISTRY }),
      "OPL_WORKSPACE_IMAGE must point to OPL_HARBOR_REGISTRY"
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
