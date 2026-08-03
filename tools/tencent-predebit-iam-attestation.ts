import { createHash, createHmac } from "node:crypto";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";

const endpoint = "https://sts.tencentcloudapi.com";
const host = "sts.tencentcloudapi.com";
const service = "sts";
const action = "GetCallerIdentity";
const version = "2018-08-13";
const algorithm = "TC3-HMAC-SHA256";
const requiredActions = ["tag:TagResources", "tag:ModifyResourcesTagValue"];

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function hmac(key, value) {
  return createHmac("sha256", key).update(value).digest();
}

function requiredString(value, code) {
  if (typeof value !== "string" || value.length === 0 || value !== value.trim()) throw new Error(code);
  return value;
}

function safeProviderCode(value) {
  return typeof value === "string" && /^[A-Za-z][A-Za-z0-9._-]{0,127}$/.test(value) ? value : "unknown";
}

function stsFailure(providerCode) {
  const error = new Error("tencent_predebit_iam_sts_failed");
  Object.defineProperty(error, "providerCode", { value: safeProviderCode(providerCode) });
  return error;
}

export function tencentPredebitIAMAttestationErrorProjection(error) {
  const errorCode = error instanceof Error && /^tencent_predebit_iam_[a-z0-9_]+$/.test(error.message)
    ? error.message
    : "tencent_predebit_iam_attestation_failed";
  const projection = { errorCode };
  if (errorCode === "tencent_predebit_iam_sts_failed") {
    projection.providerCode = safeProviderCode(error?.providerCode);
  }
  return projection;
}

function signedHeaders(secretId, secretKey, region, timestamp, body) {
  const date = new Date(timestamp * 1000).toISOString().slice(0, 10);
  const contentType = "application/json";
  const canonicalHeaders = `content-type:${contentType}\nhost:${host}\n`;
  const signedHeaderNames = "content-type;host";
  const canonicalRequest = ["POST", "/", "", canonicalHeaders, signedHeaderNames, sha256(body)].join("\n");
  const credentialScope = `${date}/${service}/tc3_request`;
  const stringToSign = [algorithm, String(timestamp), credentialScope, sha256(canonicalRequest)].join("\n");
  const secretDate = hmac(`TC3${secretKey}`, date);
  const secretService = hmac(secretDate, service);
  const secretSigning = hmac(secretService, "tc3_request");
  const signature = createHmac("sha256", secretSigning).update(stringToSign).digest("hex");
  return {
    Authorization: `${algorithm} Credential=${secretId}/${credentialScope}, SignedHeaders=${signedHeaderNames}, Signature=${signature}`,
    "Content-Type": contentType,
    Host: host,
    "X-TC-Action": action,
    "X-TC-Timestamp": String(timestamp),
    "X-TC-Version": version,
    "X-TC-Region": region
  };
}

export async function createTencentPredebitIAMAttestation({
  secretId,
  secretKey,
  region,
  releaseSha,
  policyDigest,
  timestamp = Math.floor(Date.now() / 1000),
  fetchImpl = fetch
}) {
  requiredString(secretId, "tencent_predebit_iam_secret_id_required");
  requiredString(secretKey, "tencent_predebit_iam_secret_key_required");
  requiredString(region, "tencent_predebit_iam_region_required");
  if (!/^[0-9a-f]{40}$/.test(releaseSha || "")) throw new Error("tencent_predebit_iam_release_sha_invalid");
  if (!/^sha256:[0-9a-f]{64}$/.test(policyDigest || "")) throw new Error("tencent_predebit_iam_policy_digest_invalid");
  if (!Number.isInteger(timestamp) || timestamp <= 0) throw new Error("tencent_predebit_iam_timestamp_invalid");

  const body = "{}";
  const response = await fetchImpl(endpoint, {
    method: "POST",
    headers: signedHeaders(secretId, secretKey, region, timestamp, body),
    body
  });
  let payload;
  try {
    payload = await response.json();
  } catch {
    throw new Error("tencent_predebit_iam_sts_response_invalid");
  }
  if (!response.ok || payload?.Response?.Error) throw stsFailure(payload?.Response?.Error?.Code);

  const source = payload?.Response;
  const identity = {
    type: source?.Type,
    principalId: source?.PrincipalId,
    accountId: source?.AccountId,
    userId: source?.UserId
  };
  for (const value of Object.values(identity)) requiredString(value, "tencent_predebit_iam_sts_identity_invalid");
  requiredString(source?.RequestId, "tencent_predebit_iam_sts_identity_invalid");

  return {
    schemaVersion: 1,
    proofMode: "production_runner_deployment_attestation",
    releaseSha,
    identity,
    requiredActions: [...requiredActions],
    policyDigest
  };
}

async function main() {
  const attestation = await createTencentPredebitIAMAttestation({
    secretId: process.env.TENCENTCLOUD_SECRET_ID,
    secretKey: process.env.TENCENTCLOUD_SECRET_KEY,
    region: process.env.TENCENTCLOUD_REGION,
    releaseSha: process.env.OPL_RELEASE_SHA,
    policyDigest: process.env.TENCENT_MUTATION_IAM_POLICY_DIGEST
  });
  process.stdout.write(`${JSON.stringify(attestation)}\n`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    process.stderr.write(`${JSON.stringify(tencentPredebitIAMAttestationErrorProjection(error))}\n`);
    process.exitCode = 1;
  });
}
