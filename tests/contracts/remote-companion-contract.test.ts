import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const contractPath = new URL("../../packages/contracts/opl-cloud-remote-companion-contract.json", import.meta.url);

test("remote companion credential refresh has durable exact-replay idempotency", async () => {
  const contract = JSON.parse(await readFile(contractPath, "utf8"));
  const idempotency = contract.credentials?.idempotency;

  assert.deepEqual(idempotency, {
    operationAuthority: "control_plane_runtime_operations",
    operationId: "stable_sha256_of_pairing_id_device_id_idempotency_key",
    requestIdentity: ["protocol_version", "pairing_id", "device_id", "role"],
    persistedFacts: [
      "idempotency_key_sha256",
      "request_hash",
      "issued_at",
      "usersig_expires_at",
      "provider_user_id",
      "peer_provider_user_id",
      "sdk_app_id"
    ],
    replay: "same_bound_operation_reuses_persisted_issued_at_and_reconstructs_the_exact_provider_signature_and_expiry",
    conflict: "same_operation_id_with_any_request_or_identity_drift_is_rejected_before_provider_signing",
    concurrency: "transactional_single_operation_id_create_with_loser_reload",
    restart: "operation_result_survives_control_plane_restart_without_persisting_raw_usersig"
  });
});

test("remote companion contract defines optional non-secret APNs business ID projection", async () => {
  const contract = JSON.parse(await readFile(contractPath, "utf8"));

  assert.deepEqual(contract.credentials?.pushBusinessId, {
    sourceEnv: "OPL_TENCENT_IM_APNS_BUSINESS_ID",
    wireField: "push_business_id",
    type: "positive_int32",
    secret: false,
    optional: true,
    projection: ["device_activation", "credential_refresh"]
  });
});
