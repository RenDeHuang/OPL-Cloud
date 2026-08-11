import assert from "node:assert/strict";
import test from "node:test";

import {
  createTencentPredebitIAMAttestation,
  tencentPredebitIAMAttestationErrorProjection
} from "../../tools/tencent-predebit-iam-attestation.ts";

const releaseSha = "a".repeat(40);
const policyDigest = `sha256:${"b".repeat(64)}`;
const region = "na-siliconvalley";
const identityResponse = {
  Response: {
    Type: "CAMUser",
    PrincipalId: "100000000001:123456789",
    AccountId: "100000000001",
    UserId: "123456789",
    RequestId: "req-sts-runner"
  }
};

test("production runner attestation binds a live signed STS identity to release and policy", async () => {
  const calls = [];
  const fetchImpl = async (url, init) => {
    calls.push({ url, init });
    return new Response(JSON.stringify(identityResponse), {
      status: 200,
      headers: { "content-type": "application/json" }
    });
  };

  const attestation = await createTencentPredebitIAMAttestation({
    secretId: "sid-production",
    secretKey: "skey-production",
    region,
    releaseSha,
    policyDigest,
    timestamp: 1785729600,
    fetchImpl
  });

  assert.deepEqual(attestation, {
    schemaVersion: 1,
    proofMode: "production_runner_deployment_attestation",
    releaseSha,
    identity: {
      type: "CAMUser",
      principalId: "100000000001:123456789",
      accountId: "100000000001",
      userId: "123456789"
    },
    requiredActions: ["tag:TagResources", "tag:ModifyResourcesTagValue"],
    policyDigest
  });
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, "https://sts.tencentcloudapi.com");
  assert.equal(calls[0].init.method, "POST");
  assert.equal(calls[0].init.body, "{}");
  assert.equal(calls[0].init.headers["X-TC-Action"], "GetCallerIdentity");
  assert.equal(calls[0].init.headers["X-TC-Version"], "2018-08-13");
  assert.equal(calls[0].init.headers["Content-Type"], "application/json");
  assert.equal(calls[0].init.headers["X-TC-Region"], region);
  assert.equal(calls[0].init.headers["X-TC-Timestamp"], "1785729600");
  assert.match(calls[0].init.headers.Authorization, /^TC3-HMAC-SHA256 Credential=sid-production\/.*SignedHeaders=content-type;host,/);
  assert.equal(JSON.stringify({ attestation, calls }).includes("skey-production"), false);
});

test("production runner attestation rejects invalid release and policy bindings before STS", async () => {
  let calls = 0;
  const fetchImpl = async () => {
    calls++;
    return new Response(JSON.stringify(identityResponse));
  };
  for (const input of [
    { releaseSha: "main", policyDigest },
    { releaseSha, policyDigest: `sha256:${"B".repeat(64)}` },
    { releaseSha, policyDigest: "sha256:short" }
  ]) {
    await assert.rejects(
      createTencentPredebitIAMAttestation({
        secretId: "sid-production",
        secretKey: "skey-production",
        region,
        timestamp: 1785729600,
        fetchImpl,
        ...input
      }),
      /tencent_predebit_iam_(?:release_sha|policy_digest)_invalid/
    );
  }
  assert.equal(calls, 0);
});

test("production runner attestation requires the SDK region binding before STS", async () => {
  let calls = 0;
  const fetchImpl = async () => {
    calls++;
    return new Response(JSON.stringify(identityResponse));
  };

  await assert.rejects(
    createTencentPredebitIAMAttestation({
      secretId: "sid-production",
      secretKey: "skey-production",
      releaseSha,
      policyDigest,
      timestamp: 1785729600,
      fetchImpl
    }),
    /tencent_predebit_iam_region_required/
  );
  assert.equal(calls, 0);
});

test("production runner attestation fails closed on incomplete STS identity", async () => {
  const fetchImpl = async () => new Response(JSON.stringify({
    Response: { AccountId: "100000000001", RequestId: "req-incomplete" }
  }));

  await assert.rejects(
    createTencentPredebitIAMAttestation({
      secretId: "sid-production",
      secretKey: "skey-production",
      region,
      releaseSha,
      policyDigest,
      timestamp: 1785729600,
      fetchImpl
    }),
    /tencent_predebit_iam_sts_identity_invalid/
  );
});

test("production runner attestation reports only the safe Tencent error code", async () => {
  const fetchImpl = async () => new Response(JSON.stringify({
    Response: {
      Error: {
        Code: "AuthFailure.SignatureFailure",
        Message: "signature included secret-value and must stay private"
      },
      RequestId: "request-id-must-stay-private"
    }
  }), { status: 401 });

  let projection;
  try {
    await createTencentPredebitIAMAttestation({
      secretId: "sid-production",
      secretKey: "skey-production",
      region,
      releaseSha,
      policyDigest,
      timestamp: 1785729600,
      fetchImpl
    });
    assert.fail("expected STS failure");
  } catch (error) {
    projection = tencentPredebitIAMAttestationErrorProjection(error);
  }

  assert.deepEqual(projection, {
    errorCode: "tencent_predebit_iam_sts_failed",
    providerCode: "AuthFailure.SignatureFailure"
  });
  const serialized = JSON.stringify(projection);
  assert.equal(serialized.includes("secret-value"), false);
  assert.equal(serialized.includes("request-id-must-stay-private"), false);
  assert.equal(serialized.includes("sid-production"), false);
  assert.equal(serialized.includes("skey-production"), false);
});
