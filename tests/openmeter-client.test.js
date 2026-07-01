import assert from "node:assert/strict";
import test from "node:test";

import { createMeterFromEnv, openMeterDefinitions, OpenMeterClient } from "../services/api/src/openmeter.js";

test("OpenMeterClient posts usage events with bearer auth and JSON payload", async () => {
  const requests = [];
  const client = new OpenMeterClient({
    endpoint: "https://meter.example.com",
    apiKey: "om_secret",
    fetchImpl: async (url, options) => {
      requests.push({ url, options });
      return {
        ok: true,
        status: 202,
        json: async () => ({ id: "evt_123" })
      };
    }
  });

  const result = await client.recordUsage({
    event: "workspace.server.running_hours",
    subject: "account:pi-alpha",
    value: 2,
    metadata: { workspaceId: "ws-alpha" }
  });

  assert.deepEqual(result, { ok: true, eventId: "evt_123" });
  assert.equal(requests[0].url, "https://meter.example.com/api/v1/events");
  assert.equal(requests[0].options.method, "POST");
  assert.equal(requests[0].options.headers.authorization, "Bearer om_secret");
  assert.equal(requests[0].options.headers["content-type"], "application/json");
  assert.deepEqual(JSON.parse(requests[0].options.body), {
    specversion: "1.0",
    type: "workspace.server.running_hours",
    source: "opl-cloud",
    subject: "account:pi-alpha",
    data: {
      value: 2,
      workspaceId: "ws-alpha"
    }
  });
});

test("OpenMeterClient fails closed when OpenMeter rejects an event", async () => {
  const client = new OpenMeterClient({
    endpoint: "https://meter.example.com",
    apiKey: "om_secret",
    fetchImpl: async () => ({
      ok: false,
      status: 401,
      text: async () => "unauthorized"
    })
  });

  await assert.rejects(
    client.recordUsage({
      event: "workspace.storage.gb_hours",
      subject: "account:pi-alpha",
      value: 10,
      metadata: {}
    }),
    /openmeter_event_failed:401:unauthorized/
  );
});

test("createMeterFromEnv enables OpenMeter only with endpoint and API key", () => {
  assert.equal(createMeterFromEnv({}), null);
  assert.equal(createMeterFromEnv({ OPENMETER_ENDPOINT: "https://meter.example.com" }), null);
  assert.ok(createMeterFromEnv({
    OPENMETER_ENDPOINT: "https://meter.example.com",
    OPENMETER_API_KEY: "om_secret"
  }) instanceof OpenMeterClient);
});

test("openMeterDefinitions declares production meters for OPL Cloud billing events", () => {
  assert.deepEqual(openMeterDefinitions(), {
    meters: [
      {
        slug: "opl_workspace_server_running_hours",
        eventType: "workspace.server.running_hours",
        valueProperty: "$.data.value",
        aggregation: "SUM",
        groupBy: {
          workspaceId: "$.data.workspaceId",
          accountId: "$.subject",
          packageId: "$.data.packageId",
          provider: "$.data.provider",
          serverSpec: "$.data.serverSpec"
        }
      },
      {
        slug: "opl_workspace_storage_gb_hours",
        eventType: "workspace.storage.gb_hours",
        valueProperty: "$.data.value",
        aggregation: "SUM",
        groupBy: {
          workspaceId: "$.data.workspaceId",
          accountId: "$.subject",
          packageId: "$.data.packageId",
          provider: "$.data.provider",
          diskGb: "$.data.diskGb"
        }
      }
    ]
  });
});
