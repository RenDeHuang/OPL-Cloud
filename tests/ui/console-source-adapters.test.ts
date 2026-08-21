import assert from "node:assert/strict";
import test from "node:test";

import * as readApi from "../../apps/console-ui/src/api/console-read-api.ts";
import { decodeSource } from "../../apps/console-ui/src/api/dtos.ts";
import { unavailableSource } from "../../apps/console-ui/src/app/use-console-controller.ts";
import * as workspaceApi from "../../apps/console-ui/src/api/workspaces-api.ts";

test("Workspace Gateway budget adapters use the scoped route and exact mutation boundary", async () => {
  const requests: Array<{ path: string; init?: RequestInit }> = [];
  const originalFetch = globalThis.fetch;
  const current = {
    workspaceId: "workspace / alpha",
    keyId: "9223372036854775807",
    status: "active",
    quotaUsdMicros: "9007199254740993",
    quotaUsedUsdMicros: "3000000",
    rateLimit5hUsdMicros: "500000",
    rateLimit1dUsdMicros: "1000000",
    rateLimit7dUsdMicros: "4000000",
    usage5hUsdMicros: "100000",
    usage1dUsdMicros: "200000",
    usage7dUsdMicros: "300000",
    enabled: true,
    updatedAt: "2026-08-19T01:02:03Z"
  };
  globalThis.fetch = async (input, init) => {
    requests.push({ path: String(input), init });
    return new Response(JSON.stringify({
      source: "sub2api",
      status: "available",
      available: true,
      fetchedAt: "2026-08-19T01:02:04Z",
      data: init?.method === "PATCH" ? { ...current, status: "disabled", enabled: false } : current
    }), { status: 200, headers: { "content-type": "application/json" } });
  };

  try {
    const readback = await workspaceApi.getWorkspaceGatewayBudget("workspace / alpha", "9223372036854775807");
    const expectedPayload = {
      quotaUsdMicros: 9_000_000,
      rateLimit5hUsdMicros: 500_000,
      rateLimit1dUsdMicros: 1_000_000,
      rateLimit7dUsdMicros: 4_000_000,
      enabled: false,
      resetQuota: true,
      resetRateLimitUsage: true
    };
    const input = { ...expectedPayload, name: "must-not-pass", groupId: "must-not-pass" };
    const updated = await workspaceApi.updateWorkspaceGatewayBudget(
      "workspace / alpha",
      "9223372036854775807",
      input,
      "csrf-budget",
      "workspace-budget:opaque"
    );

    assert.equal(readback.available && readback.data.quotaUsdMicros, "9007199254740993");
    assert.equal(updated.available && updated.data.enabled, false);
    assert.deepEqual(requests.map(({ path }) => path), [
      "/api/workspaces/workspace%20%2F%20alpha/gateway-budget",
      "/api/workspaces/workspace%20%2F%20alpha/gateway-budget"
    ]);
    assert.equal(requests[0].init?.method, undefined);
    assert.equal(requests[1].init?.method, "PATCH");
    assert.equal(new Headers(requests[1].init?.headers).get("content-type"), "application/json");
    assert.equal(new Headers(requests[1].init?.headers).get("x-opl-csrf"), "csrf-budget");
    assert.equal(new Headers(requests[1].init?.headers).get("Idempotency-Key"), "workspace-budget:opaque");
    assert.deepEqual(JSON.parse(String(requests[1].init?.body)), expectedPayload);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("Workspace Gateway budget adapter rejects source, identity, field, and micros drift", async () => {
  const base = {
    workspaceId: "ws-alpha",
    keyId: "19",
    status: "active",
    quotaUsdMicros: "9000000",
    quotaUsedUsdMicros: "3000000",
    rateLimit5hUsdMicros: "500000",
    rateLimit1dUsdMicros: "1000000",
    rateLimit7dUsdMicros: "4000000",
    usage5hUsdMicros: "100000",
    usage1dUsdMicros: "200000",
    usage7dUsdMicros: "300000",
    enabled: true,
    updatedAt: "2026-08-19T01:02:03Z"
  };
  const cases = [
    { source: "control-plane", data: base },
    { source: "sub2api", data: { ...base, workspaceId: "ws-other" } },
    { source: "sub2api", data: { ...base, keyId: "20" } },
    { source: "sub2api", data: { ...base, quotaUsdMicros: 9_000_000 } },
    { source: "sub2api", data: { ...base, quotaUsdMicros: "9223372036854775808" } },
    { source: "sub2api", data: { ...base, unexpected: true } }
  ];

  for (const item of cases) {
    globalThis.fetch = async () => new Response(JSON.stringify({
      source: item.source,
      status: "available",
      available: true,
      fetchedAt: "2026-08-19T01:02:04Z",
      data: item.data
    }), { status: 200, headers: { "content-type": "application/json" } });
    await assert.rejects(
      workspaceApi.getWorkspaceGatewayBudget("ws-alpha", "19"),
      /invalid_workspace_gateway_budget_source/
    );
  }
});

test("unavailable source adapters preserve the authoritative reason code", () => {
  const source = decodeSource({
    source: "sub2api",
    status: "unavailable",
    available: false,
    fetchedAt: "2026-08-02T00:00:00Z",
    sourceUpdatedAt: "2026-08-01T23:59:59Z",
    reasonCode: "sub2api_unavailable"
  });

  assert.deepEqual(source, {
    source: "sub2api",
    status: "unavailable",
    available: false,
    fetchedAt: "2026-08-02T00:00:00Z",
    sourceUpdatedAt: "2026-08-01T23:59:59Z",
    reasonCode: "sub2api_unavailable"
  });
});

test("unavailable source adapters reject a missing reason code", () => {
  assert.throws(() => decodeSource({
    source: "sub2api",
    status: "unavailable",
    available: false,
    fetchedAt: "2026-08-02T00:00:00Z"
  }), /invalid_source_envelope/);
});

test("source adapters reject contradictory availability states", () => {
  assert.throws(() => decodeSource({
    source: "sub2api",
    status: "available",
    available: false,
    fetchedAt: "2026-08-02T00:00:00Z",
    reasonCode: "sub2api_unavailable"
  }), /invalid_source_envelope/);
  assert.throws(() => decodeSource({
    source: "sub2api",
    status: "unavailable",
    available: true,
    fetchedAt: "2026-08-02T00:00:00Z",
    reasonCode: "sub2api_unavailable"
  }), /invalid_source_envelope/);
});

test("unavailable source adapters reject data disguised as an unavailable source", () => {
  assert.throws(() => decodeSource({
    source: "sub2api",
    status: "unavailable",
    available: false,
    fetchedAt: "2026-08-02T00:00:00Z",
    reasonCode: "sub2api_unavailable",
    data: { total: 0, items: [] }
  }), /invalid_source_envelope/);
});

test("source adapters require non-empty source and fetchedAt fields", () => {
  for (const input of [
    { source: "", status: "empty", available: true, fetchedAt: "2026-08-02T00:00:00Z", data: [] },
    { source: "sub2api", status: "empty", available: true, fetchedAt: "", data: [] }
  ]) {
    assert.throws(() => decodeSource(input), /invalid_source_envelope/);
  }
});

test("local unavailable fallbacks keep a stable reason code and a real fetch timestamp", () => {
  const fallback = unavailableSource("Control Plane + Ledger");
  assert.equal(fallback.status, "unavailable");
  assert.equal(fallback.reasonCode, "control_plane_ledger_unavailable");
  assert.ok(fallback.fetchedAt);
  assert.ok(Number.isFinite(Date.parse(fallback.fetchedAt)));
});

test("Console read adapters normalize a legacy unavailable envelope with a stable reason code", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async () => new Response(JSON.stringify({
    source: "control-plane+fabric+ledger",
    status: "unavailable",
    available: false,
    fetchedAt: "2026-08-02T00:00:00Z"
  }), { status: 502, headers: { "content-type": "application/json" } })) as typeof fetch;

  try {
    assert.deepEqual(await readApi.getBillingReceipts(), {
      source: "control-plane+fabric+ledger",
      status: "unavailable",
      available: false,
      fetchedAt: "2026-08-02T00:00:00Z",
      reasonCode: "control_plane_fabric_ledger_unavailable"
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("per-Key usage list and summary send the same canonical period", async () => {
  const requested: string[] = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async (input: string | URL | Request) => {
    requested.push(String(input));
    return new Response(JSON.stringify({
      source: "sub2api",
      status: "empty",
      available: true,
      fetchedAt: "2026-08-02T00:00:00Z",
      data: { items: [], total: 0, page: 1, pageSize: 20, pages: 1 }
    }), { status: 200, headers: { "content-type": "application/json" } });
  }) as typeof fetch;

  try {
    await readApi.getGatewayKeyUsage("key / 1", 1, 20, "today");
    await readApi.getGatewayKeyUsageSummary("key / 1", "today");
    assert.deepEqual(requested, [
      "/api/gateway/keys/key%20%2F%201/usage?page=1&pageSize=20&period=today",
      "/api/gateway/keys/key%20%2F%201/usage-summary?period=today"
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("Workspace adapters find an exact ID through real server pagination and stop when found", async () => {
  const requested: string[] = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async (input: string | URL | Request) => {
    const path = String(input);
    requested.push(path);
    const page = Number(new URL(path, "https://console.invalid").searchParams.get("page"));
    const item = (id: string) => ({
      id, ownerAccountId: "acct-1", ownerUserId: "user-1", state: "active",
      createdAt: "2026-07-01T00:00:00Z", updatedAt: "2026-07-01T00:00:00Z"
    });
    const data = {
      items: page === 1 ? [item("ws-1")] : page === 2 ? [item("ws / alpha")] : [item("ws-3")],
      total: 3,
      page,
      pageSize: 1
    };
    return new Response(JSON.stringify({
      source: "control-plane", status: "available",
      available: true, fetchedAt: "2026-07-01T00:00:00Z", data
    }), { status: 200, headers: { "content-type": "application/json" } });
  }) as typeof fetch;

  try {
    const detail = await workspaceApi.findWorkspaceInPages("ws / alpha", 1);
    assert.equal(detail.available && detail.data.id, "ws / alpha");
    assert.deepEqual(requested, [
      "/api/workspaces?page=1&pageSize=1",
      "/api/workspaces?page=2&pageSize=1"
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("Workspace adapters return an authoritative not-found result after total is exhausted", async () => {
  const requested: string[] = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async (input: string | URL | Request) => {
    const path = String(input);
    requested.push(path);
    const page = Number(new URL(path, "https://console.invalid").searchParams.get("page"));
    const items = page === 1
      ? [{ id: "ws-1", ownerAccountId: "acct-1", ownerUserId: "user-1", state: "active", createdAt: "2026-07-01T00:00:00Z", updatedAt: "2026-07-01T00:00:00Z" }]
      : [{ id: "ws-2", ownerAccountId: "acct-1", ownerUserId: "user-1", state: "active", createdAt: "2026-07-01T00:00:00Z", updatedAt: "2026-07-01T00:00:00Z" }];
    return new Response(JSON.stringify({
      source: "control-plane", status: "available", available: true,
      fetchedAt: "2026-07-01T00:00:00Z", data: { items, total: 2, page, pageSize: 1 }
    }), { status: 200, headers: { "content-type": "application/json" } });
  }) as typeof fetch;

  try {
    const detail = await workspaceApi.findWorkspaceInPages("ws-missing", 1);
    assert.equal(detail.available, true);
    assert.equal(detail.status, "empty");
    assert.equal(detail.data, null);
    assert.deepEqual(requested, [
      "/api/workspaces?page=1&pageSize=1",
      "/api/workspaces?page=2&pageSize=1"
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
