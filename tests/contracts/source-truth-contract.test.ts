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
  assert.deepEqual(Object.keys(gateway), [
    "endpoint", "wallet", "keys", "usage", "usageStats", "accountUsageStats", "balanceHistory"
  ]);
  assert.deepEqual(Object.values(gateway).map((source: any) => source.route), [
    "GET /api/gateway/endpoint",
    "GET /api/gateway/wallet",
    "GET /api/gateway/keys",
    "GET /api/gateway/keys/{keyId}/usage",
    "GET /api/gateway/keys/{keyId}/usage-summary",
    "GET /api/gateway/usage-summary",
    "GET /api/gateway/balance-history"
  ]);
  assert.deepEqual(gateway.endpoint, {
    route: "GET /api/gateway/endpoint",
    source: "control-plane",
    authority: "OPL_GATEWAY_PUBLIC_BASE_URL",
    dataFields: ["baseUrl"],
    productionScheme: "https",
    missingBehavior: "unavailable_without_data",
    forbiddenFallbacks: ["OPL_SUB2API_BASE_URL", "gflabtoken.cn"],
    fetchedAt: "control_plane_response_fetch_completion_time",
    sourceUpdatedAt: "omit_no_authoritative_source_timestamp"
  });
  for (const source of [
    gateway.wallet, gateway.keys, gateway.usage, gateway.usageStats,
    gateway.accountUsageStats, gateway.balanceHistory
  ]) {
    assert.equal(source.source, "sub2api");
    assert.equal(source.identity, "session_account_to_control_plane_sub2api_user_mapping");
    assert.deepEqual(source.ignoredIdentityInputs, ["accountId", "user_id", "api_key_id", "sub2apiUserId"]);
    assert.equal(source.fetchedAt, "control_plane_response_fetch_completion_time");
    assert.equal(source.sourceUpdatedAt, "omit_unless_sub2api_returns_source_timestamp");
  }
  assert.equal(gateway.accountUsageStats.authority, "live_sub2api_aggregate");
  for (const source of [gateway.wallet, gateway.keys, gateway.usage, gateway.usageStats, gateway.balanceHistory]) {
    assert.equal(source.authority, "live_sub2api_readback");
  }
  assert.deepEqual(gateway.wallet.dataFields, ["userId", "currency", "usdMicros", "status"]);
  assert.deepEqual(gateway.keys.itemFields, [
    "id", "name", "kind", "status", "quotaUsdMicros", "quotaUsedUsdMicros",
    "usage5hUsdMicros", "usage1dUsdMicros", "usage7dUsdMicros", "lastUsedAt",
    "expiresAt", "manageable", "deletable"
  ]);
  assert.equal(gateway.keys.idFormat, "positive_decimal_string");
  assert.deepEqual(gateway.keys.statusValues, ["active", "disabled"]);
  assert.equal(gateway.keys.empty, "real_zero_rows");
  assert.deepEqual(gateway.keys.lifecycleRoutes, [
    "POST /api/gateway/keys",
    "PATCH /api/gateway/keys/{keyId}",
    "DELETE /api/gateway/keys/{keyId}"
  ]);
  assert.deepEqual(gateway.keys.createRequest, {
    fields: ["name", "quotaUsdMicros", "expiresInDays"],
    expiryField: "expiresInDays",
    upstreamWrites: 1,
    responseExpiryField: "expiresAt"
  });
  assert.equal(gateway.keys.revealRoute, "POST /api/gateway/keys/{keyId}/reveal");
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
  assert.deepEqual(gateway.accountUsageStats.dataFields, gateway.usageStats.dataFields);
  assert.equal(gateway.accountUsageStats.aggregation, "upstream_only_never_current_page_sum");
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
    authority: "control_plane_mapping_plus_paginated_and_batch_sub2api_readback",
    scope: "customer_accounts_only",
    itemFields: ["accountId", "consoleUserId", "role", "sub2apiUserId", "email", "status"],
    nestedSourceFields: ["gatewayIdentity", "wallet", "keyCount", "usage", "workspaceCount"],
    fieldAuthority: {
      accountId: "control_plane",
      consoleUserId: "control_plane",
      role: "control_plane",
      sub2apiUserId: "control_plane_verified_mapping",
      email: "control_plane_verified_mapping",
      status: "control_plane_user_lifecycle",
      gatewayIdentity: "sub2api_paginated_user_readback",
      wallet: "sub2api_paginated_user_readback",
      usage: "sub2api_batch_user_usage",
      workspaceCount: "control_plane",
      keyCount: "unavailable_until_authoritative_batch_count_exists"
    },
    sub2apiUserIdFormat: "positive_decimal_string",
    statusValues: ["active", "disabled"],
    mappingConsistency: "remote_id_and_normalized_email_must_equal_control_plane_mapping",
    pagination: "one_bounded_sub2api_user_page_then_control_plane_page",
    batchSizeMax: 50,
    perAccountUserOrUsageNPlusOne: false,
    failure: "affected_nested_source_unavailable_without_zero_data",
    fetchedAt: "control_plane_response_fetch_completion_time",
    sourceUpdatedAt: "sub2api_user_updated_at_only_for_corresponding_nested_facts"
  });

  assert.deepEqual(contract.sources.workspace, {
    list: {
      route: "GET /api/workspaces",
      source: "control-plane",
      authority: "control_plane_workspace_table",
      identity: "session_account",
      requiredItemFields: ["id", "ownerAccountId", "ownerUserId", "state", "createdAt", "updatedAt"],
      optionalItemFields: [
        "name", "url", "storageId", "currentComputeAllocationId", "currentAttachmentId", "runtimeId", "workspaceApiKeyId",
        "packageId", "storageGb", "autoRenew", "priceVersion", "currency", "totalUsdMicros",
        "periodStart", "paidThrough", "renewalStatus"
      ],
      forbiddenFields: ["accountId", "status", "compute", "storage", "attachment", "runtime", "access", "provider", "checks"],
      empty: "real_zero_rows",
      fetchedAt: "control_plane_response_fetch_completion_time",
      sourceUpdatedAt: "omit_without_authoritative_table_source_timestamp"
    },
    runtimeStatus: {
      route: "GET /api/workspaces/{workspaceId}/runtime-status",
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
    },
    files: {
      route: "GET /api/workspaces/{workspaceId}/files",
      source: "runtime",
      authority: "workspace_runtime_projects_mount",
      identity: "session_owned_workspace",
      persistence: "none",
      fetchedAt: "control_plane_response_fetch_completion_time",
      sourceUpdatedAt: "only_when_runtime_returns_source_timestamp"
    },
    filesystemUsage: {
      route: "GET /api/workspaces/{workspaceId}/filesystem-usage",
      source: "runtime",
      authority: "workspace_runtime_statfs",
      identity: "session_owned_workspace",
      persistence: "none",
      fetchedAt: "control_plane_response_fetch_completion_time",
      sourceUpdatedAt: "only_when_runtime_returns_measurement_timestamp"
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
        "priceVersion", "currency", "periodStart", "paidThrough", "totalUsdMicros", "chargeReference",
        "components", "fulfillment", "refundUsdMicros"
      ],
      moneyFieldsByType: {
        resource: ["chargeUsdMicros"],
        workspacePurchased: ["totalUsdMicros"],
        workspaceRenewed: ["totalUsdMicros"],
        workspaceExpired: ["totalUsdMicros"],
        workspaceRefunded: ["totalUsdMicros", "refundUsdMicros"]
      },
      workspaceChargeReferenceTypes: ["billing.workspace_purchased.v1", "billing.workspace_renewed.v1", "billing.workspace_refunded.v1"],
      workspaceFulfillmentReceiptTypes: ["billing.workspace_purchased.v1", "billing.workspace_renewed.v1"],
      workspaceFulfillmentFields: ["computeAllocationId", "storageId", "attachmentId", "workspaceApiKeyId", "runtimeId"],
      rawProviderReadback: false,
      nonBillingRows: "ignore_without_projection",
      tenantMismatch: "whole_source_unavailable",
      malformedBillingReceipt: "whole_source_unavailable",
      legacyCnyFallback: false,
      pagination: "preserve_ledger_cursor_and_has_more",
      empty: "real_zero_billing_rows",
      fetchedAt: "control_plane_response_fetch_completion_time",
      sourceUpdatedAt: "omit_unless_ledger_returns_source_timestamp"
    },
    workspaceCreatedReceipt: {
      route: "GET /api/billing/receipts/{id}",
      source: "ledger",
      authority: "live_ledger_readback",
      identity: "session_account_and_path_receipt_id",
      ignoredIdentityInputs: ["accountId"],
      dataFields: ["receiptId", "type", "status", "workspaceId", "createdAt"],
      fixedType: "workspace.created",
      fixedStatus: "completed",
      listProjection: false,
      tenantMismatch: "not_found",
      malformedReceipt: "whole_source_unavailable",
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
