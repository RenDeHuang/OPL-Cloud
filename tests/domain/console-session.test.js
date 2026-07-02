import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../services/api/src/opl-cloud.js";
import { MemoryStore } from "../../services/api/src/store.js";

const pricing = {
  serverHourly: { basic: 1, pro: 4 },
  diskGbMonth: 0.2,
  markup: 0.2
};

test("console session persists the selected account profile and role", async () => {
  const store = new MemoryStore();
  const service = createOplCloud({
    store,
    runtimeProvider: { name: "test-provider" },
    pricing
  });

  const session = await service.updateConsoleSession({
    accountId: "pi-beta",
    tenantId: "tenant-beta",
    displayName: "Beta Lab PI",
    email: "pi-beta@example.com",
    role: "pi"
  });

  assert.deepEqual(session, {
    accountId: "pi-beta",
    tenantId: "tenant-beta",
    displayName: "Beta Lab PI",
    email: "pi-beta@example.com",
    role: "pi"
  });

  const persisted = await store.read();
  assert.equal(persisted.consoleSession.accountId, "pi-beta");
  assert.equal(persisted.accounts["pi-beta"].displayName, "Beta Lab PI");
  assert.equal(persisted.accounts["pi-beta"].email, "pi-beta@example.com");
  assert.equal(persisted.accounts["pi-beta"].tenantId, "tenant-beta");
  assert.equal(persisted.accounts["pi-beta"].role, "pi");

  const state = await service.getState("pi-beta");
  assert.equal(state.account.id, "pi-beta");
  assert.equal(state.account.displayName, "Beta Lab PI");
  assert.equal(state.account.tenantId, "tenant-beta");
});

