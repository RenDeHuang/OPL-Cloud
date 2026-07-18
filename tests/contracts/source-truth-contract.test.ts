import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const contractUrl = new URL("../../packages/contracts/opl-cloud-console-source-truth-contract.json", import.meta.url);

test("Console source truth contract fixes strict envelopes and live Gateway projections", async () => {
  const contract = JSON.parse(await readFile(contractUrl, "utf8"));

  assert.equal(contract.state, "current");
  assert.deepEqual(contract.envelope.requiredFields, ["source", "status", "available", "fetchedAt"]);
  assert.deepEqual(contract.envelope.statuses, ["available", "empty", "unavailable"]);
  assert.deepEqual(contract.envelope.states, {
    available: { available: true, data: "required" },
    empty: { available: true, data: "required_real_zero_rows" },
    unavailable: { available: false, data: "forbidden", zeroValues: "forbidden", emptyCollections: "forbidden" }
  });
  assert.equal(contract.envelope.sourceUpdatedAt, "optional_only_when_returned_by_authoritative_upstream");
  assert.equal(contract.envelope.fetchedAtMaySubstituteSourceUpdatedAt, false);
  assert.deepEqual(contract.envelope.httpSemantics, {
    upstreamUnavailable: [500, 502],
    authentication: [401, 403],
    unavailableBody: "strict_unavailable_envelope"
  });

  const gateway = contract.sources.gateway;
  assert.deepEqual(Object.keys(gateway), ["wallet", "keys", "usage", "usageStats", "balanceHistory"]);
  assert.deepEqual(Object.values(gateway).map((source: any) => source.route), [
    "GET /api/gateway/wallet",
    "GET /api/gateway/keys",
    "GET /api/gateway/usage",
    "GET /api/gateway/usage/stats",
    "GET /api/gateway/balance-history"
  ]);
  for (const source of Object.values(gateway) as any[]) {
    assert.equal(source.source, "sub2api");
    assert.equal(source.authority, "live_sub2api_readback");
    assert.equal(source.identity, "session_account_to_control_plane_sub2api_user_mapping");
    assert.deepEqual(source.ignoredIdentityInputs, ["accountId", "user_id", "api_key_id", "sub2apiUserId"]);
    assert.equal(source.fetchedAt, "control_plane_response_fetch_completion_time");
    assert.equal(source.sourceUpdatedAt, "omit_unless_sub2api_returns_source_timestamp");
  }
  assert.deepEqual(gateway.wallet.dataFields, ["userId", "currency", "usdMicros", "status"]);
  assert.deepEqual(gateway.keys.itemFields, [
    "id", "name", "status", "quotaUsdMicros", "quotaUsedUsdMicros",
    "usage5hUsdMicros", "usage1dUsdMicros", "usage7dUsdMicros", "lastUsedAt"
  ]);
  assert.equal(gateway.keys.idFormat, "positive_decimal_string");
  assert.deepEqual(gateway.keys.statusValues, ["active", "disabled"]);
  assert.equal(gateway.keys.empty, "real_zero_rows");
  assert.equal(gateway.keys.revealRoute, "POST /api/gateway/keys/opl-workspace/reveal");
  assert.equal(gateway.keys.revealAuthorization, "session_owner_with_csrf");
  assert.deepEqual(gateway.keys.revealDataFields, ["id", "name", "status", "value"]);
  assert.equal(gateway.keys.revealIdFormat, "positive_decimal_string");
  assert.equal(gateway.keys.revealCacheControl, "private, no-store");
  assert.equal(gateway.keys.revealSecretBoundary, "dedicated_response_only");
  assert.deepEqual(gateway.usage.itemFields, [
    "apiKeyId", "requestId", "createdAt", "model", "inboundEndpoint", "requestType",
    "inputTokens", "outputTokens", "cacheCreationTokens", "cacheReadTokens", "actualCostUsdMicros"
  ]);
  assert.equal(gateway.usage.apiKeyIdFormat, "positive_decimal_string");
  assert.deepEqual(gateway.usageStats.dataFields, [
    "totalRequests", "totalInputTokens", "totalOutputTokens", "totalTokens", "totalActualCostUsdMicros"
  ]);
  assert.equal(gateway.usageStats.zeroAggregate, "available");
  assert.deepEqual(gateway.balanceHistory.itemFields, ["type", "valueUsdMicros", "status", "usedAt", "createdAt"]);

  const identity = contract.sources.identity;
  assert.deepEqual(identity.authMe, {
    route: "GET /api/auth/me",
    source: "sub2api",
    authority: "live_sub2api_readback",
    sessionFields: ["consoleUserId", "accountId", "role"],
    remoteFields: ["sub2apiUserId", "email", "status"],
    fieldAuthority: {
      consoleUserId: "control_plane_session",
      accountId: "control_plane_session",
      role: "control_plane_session",
      sub2apiUserId: "sub2api",
      email: "sub2api",
      status: "sub2api"
    },
    sub2apiUserIdFormat: "positive_decimal_string",
    statusValues: ["active", "disabled"],
    mappingConsistency: "remote_id_and_normalized_email_must_equal_control_plane_mapping",
    legacyRoute: "GET /api/me returns 404",
    fetchedAt: "control_plane_response_fetch_completion_time",
    sourceUpdatedAt: "omit_unless_sub2api_returns_source_timestamp"
  });
  assert.deepEqual(identity.operatorAccounts, {
    route: "GET /api/operator/accounts",
    authorization: "operator_only",
    source: "control-plane+sub2api",
    authority: "control_plane_mapping_plus_sequential_live_sub2api_readback",
    scope: "customer_accounts_only",
    itemFields: ["accountId", "consoleUserId", "role", "sub2apiUserId", "email", "status"],
    fieldAuthority: {
      accountId: "control_plane",
      consoleUserId: "control_plane",
      role: "control_plane",
      sub2apiUserId: "sub2api",
      email: "sub2api",
      status: "sub2api"
    },
    sub2apiUserIdFormat: "positive_decimal_string",
    statusValues: ["active", "disabled"],
    mappingConsistency: "remote_id_and_normalized_email_must_equal_control_plane_mapping",
    failure: "whole_source_unavailable_without_partial_data",
    fetchedAt: "control_plane_response_fetch_completion_time",
    sourceUpdatedAt: "omit_unless_sub2api_returns_source_timestamp"
  });

  assert.deepEqual(contract.sources.workspace, {
    list: {
      route: "GET /api/workspaces",
      source: "control-plane",
      authority: "control_plane_workspace_table",
      identity: "session_account",
      requiredItemFields: ["id", "ownerAccountId", "ownerUserId", "state", "createdAt", "updatedAt"],
      optionalItemFields: [
        "name", "url", "storageId", "currentComputeAllocationId", "currentAttachmentId", "runtimeId",
        "packageId", "storageGb", "autoRenew", "priceVersion", "currency", "totalUsdMicros",
        "periodStart", "paidThrough", "renewalStatus"
      ],
      forbiddenFields: ["accountId", "status", "compute", "storage", "attachment", "runtime", "access", "provider", "checks"],
      empty: "real_zero_rows",
      fetchedAt: "control_plane_response_fetch_completion_time",
      sourceUpdatedAt: "omit_without_authoritative_table_source_timestamp"
    },
    runtimeStatus: {
      route: "POST /api/workspaces/runtime-status",
      source: "fabric",
      authority: "live_fabric_runtime_status",
      identity: "session_owned_workspace",
      requiredDataFields: ["workspaceId", "status", "ready", "checks"],
      optionalDataFields: ["runtimeId", "url", "serviceName", "access"],
      conditionalDataFields: {
        runningOrUnready: ["runtimeId", "url", "serviceName"],
        notFoundOrDestroyed: "project_only_when_present"
      },
      accessFields: ["username", "credentialStatus", "credentialVersion"],
      checkFields: ["name", "ok"],
      statusValues: ["running", "unready", "not_found", "destroyed"],
      emptyChecks: "preserve_real_empty_array",
      forbiddenFields: ["provider", "password", "secretRef"],
      inferredStatus: false,
      writeBack: false,
      fetchedAt: "control_plane_response_fetch_completion_time",
      sourceUpdatedAt: "omit_unless_fabric_returns_source_timestamp"
    }
  });
  assert.deepEqual(contract.sources.ledger, {
    billingReceipts: {
      routes: ["GET /api/billing/receipts", "GET /api/billing/receipts/{id}"],
      source: "ledger",
      authority: "live_ledger_readback",
      identity: "session_account",
      ignoredIdentityInputs: ["accountId"],
      itemFields: [
        "receiptId", "type", "status", "workspaceId", "createdAt", "resourceType", "resourceId",
        "priceVersion", "currency", "periodStart", "paidThrough"
      ],
      moneyFieldsByType: {
        resource: ["chargeUsdMicros"],
        workspaceRenewed: ["totalUsdMicros"],
        workspaceExpired: ["totalUsdMicros"],
        workspaceRefunded: ["totalUsdMicros", "refundUsdMicros"]
      },
      nonBillingRows: "ignore_without_projection",
      tenantMismatch: "whole_source_unavailable",
      malformedBillingReceipt: "whole_source_unavailable",
      legacyCnyFallback: false,
      pagination: "preserve_ledger_cursor_and_has_more",
      empty: "real_zero_billing_rows",
      fetchedAt: "control_plane_response_fetch_completion_time",
      sourceUpdatedAt: "omit_unless_ledger_returns_source_timestamp"
    }
  });
  assert.deepEqual(contract.forbiddenCustomerFields, [
    "key", "secret", "notes", "raw", "rawAdminDTO", "prompt", "responseContent"
  ]);

  assert.deepEqual(contract.deploymentEvidence.sub2api, {
    anonymousProbeStatus: 401,
    proves: "route_presence_only",
    authenticatedSchema: "pending_13B"
  });
});
