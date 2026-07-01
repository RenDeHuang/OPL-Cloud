import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { createOplCloud } from "../../services/api/src/opl-cloud.js";
import { LocalDockerProvider } from "../../services/api/src/runtime-providers/local-docker.js";
import { MemoryStore } from "../../services/api/src/store.js";

async function createService({ meter = null } = {}) {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-meter-"));
  const events = [];
  const service = createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: new LocalDockerProvider({
      rootDir: root,
      baseUrl: "http://127.0.0.1:8787",
      execute: false
    }),
    pricing: {
      serverHourly: { basic: 1, pro: 4 },
      diskGbMonth: 0.2,
      markup: 0.1
    },
    meter: meter ?? {
      async recordUsage(event) {
        events.push(event);
        return { ok: true, eventId: `meter-${events.length}` };
      }
    }
  });
  return {
    service,
    events,
    cleanup: () => rm(root, { recursive: true, force: true })
  };
}

test("settleBilling emits OpenMeter usage events for server hours and storage GB hours", async () => {
  const { service, events, cleanup } = await createService();
  try {
    await service.creditAccount({ accountId: "pi-alpha", amount: 200, reason: "meter_credit" });
    const workspace = await service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "Metered Lab",
      packageId: "basic"
    });

    const settlement = await service.settleBilling({
      accountId: "pi-alpha",
      workspaceId: workspace.id,
      hours: 3,
      sourceEventId: "meter_tick_openmeter"
    });

    assert.deepEqual(events.map((event) => event.event), [
      "workspace.server.running_hours",
      "workspace.storage.gb_hours"
    ]);
    assert.deepEqual(events.map((event) => event.value), [3, 30]);
    assert.deepEqual(events.map((event) => event.subject), [`account:pi-alpha`, `account:pi-alpha`]);
    assert.equal(events[0].metadata.workspaceId, workspace.id);
    assert.equal(events[0].metadata.packageId, "basic");
    assert.equal(events[0].metadata.sourceEventId, "meter_tick_openmeter");
    assert.equal(events[1].metadata.diskGb, 10);
    assert.equal(settlement.metering.length, 2);
    assert.deepEqual(settlement.metering.map((item) => item.ok), [true, true]);
  } finally {
    await cleanup();
  }
});

test("settleBilling keeps local ledger closed when metering is not configured", async () => {
  const { service, cleanup } = await createService({ meter: null });
  service.meter = null;
  try {
    await service.creditAccount({ accountId: "pi-alpha", amount: 200, reason: "no_meter_credit" });
    const workspace = await service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "No Meter Lab",
      packageId: "basic"
    });

    const settlement = await service.settleBilling({
      accountId: "pi-alpha",
      workspaceId: workspace.id,
      hours: 1,
      sourceEventId: "meter_tick_disabled"
    });

    assert.equal(settlement.entries.length, 2);
    assert.deepEqual(settlement.metering, []);
  } finally {
    await cleanup();
  }
});
