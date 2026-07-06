import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("..", import.meta.url));

export const defaultStagingEnvPath = join(root, ".env.staging.local");

export const requiredStagingLocalEnv = Object.freeze([
  "OPL_RUNTIME_PROVIDER",
  "DATABASE_URL",
  "OPL_WORKSPACE_IMAGE",
  "OPL_WORKSPACE_DOMAIN",
  "OPL_K8S_NAMESPACE",
  "OPL_INGRESS_CLASS",
  "OPL_WORKSPACE_STORAGE_CLASS",
  "OPL_IMAGE_PULL_SECRET_NAME",
  "OPL_TENCENT_PROVISIONER_BIN",
  "TENCENTCLOUD_SECRET_ID",
  "TENCENTCLOUD_SECRET_KEY",
  "TENCENTCLOUD_REGION",
  "TENCENT_DEPLOY_CLUSTER_ID",
  "TENCENT_DEPLOY_KUBECONFIG_REF",
  "TENCENT_CVM_SUBNET_ID",
  "TENCENT_CVM_SECURITY_GROUP_IDS",
  "OPL_BASIC_COMPUTE_INSTANCE_TYPE"
]);

function unquote(value) {
  const trimmed = String(value || "").trim();
  if (
    (trimmed.startsWith("\"") && trimmed.endsWith("\"")) ||
    (trimmed.startsWith("'") && trimmed.endsWith("'"))
  ) {
    return trimmed.slice(1, -1);
  }
  return trimmed;
}

export function parseEnvText(text) {
  const values = {};
  for (const rawLine of String(text || "").split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) continue;
    const [key, ...rest] = line.split("=");
    const name = key.trim();
    if (!name) continue;
    values[name] = unquote(rest.join("="));
  }
  return values;
}

export function loadEnvFile({ filePath = defaultStagingEnvPath, baseEnv = process.env, requireFile = true } = {}) {
  if (!existsSync(filePath)) {
    if (requireFile) throw new Error(`staging_env_file_missing:${filePath}`);
    return { ...baseEnv };
  }
  const parsed = parseEnvText(readFileSync(filePath, "utf8"));
  return {
    ...parsed,
    ...baseEnv
  };
}

function looksLikePublicDomain(value) {
  const domain = String(value || "");
  return Boolean(
    domain &&
    domain.includes(".") &&
    !domain.includes("localhost") &&
    !domain.startsWith("127.") &&
    !domain.startsWith("0.0.0.0")
  );
}

export function validateStagingLocalEnv(env = process.env) {
  const missingEnv = requiredStagingLocalEnv.filter((key) => !String(env[key] || "").trim());
  const checks = [
    {
      id: "runtime_provider",
      ok: env.OPL_RUNTIME_PROVIDER === "tencent-tke",
      message: "local-to-staging must run with OPL_RUNTIME_PROVIDER=tencent-tke"
    },
    {
      id: "database_url",
      ok: Boolean(env.DATABASE_URL),
      message: "local-to-staging must use the shared staging PostgreSQL DATABASE_URL"
    },
    {
      id: "workspace_domain",
      ok: looksLikePublicDomain(env.OPL_WORKSPACE_DOMAIN),
      message: "local-to-staging must use the public staging Workspace domain"
    },
    {
      id: "provisioner",
      ok: Boolean(env.OPL_TENCENT_PROVISIONER_BIN),
      message: "local-to-staging must point at the Go Tencent provisioner binary"
    }
  ];
  const failedChecks = checks.filter((check) => !check.ok).map((check) => check.id);
  return {
    ready: missingEnv.length === 0 && failedChecks.length === 0,
    missingEnv,
    failedChecks,
    checks
  };
}

export function applyEnv(env) {
  for (const [key, value] of Object.entries(env)) {
    process.env[key] = value;
  }
}
