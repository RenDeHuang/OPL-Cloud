import assert from "node:assert/strict";
import test from "node:test";

import {
  createCurrentTestService,
  provisionWorkspace
} from "../helpers/current-resource-chain.js";

test("Workspace access uses a long-lived URL token that can be deleted and reset after leakage", async () => {
  const service = createCurrentTestService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });
  const { workspace } = await provisionWorkspace(service, {
    accountId: "pi-alpha",
    userId: "usr-alpha",
    workspaceName: "Token Lab",
    packageId: "basic"
  });

  assert.equal(workspace.access.mode, "long_lived_url_token");
  assert.equal(workspace.access.requiresLogin, false);
  assert.equal(workspace.access.tokenStatus, "active");
  assert.equal(workspace.access.rotationPolicy, "reset_or_delete_on_leak");

  const resolved = await service.resolveWorkspaceAccess({
    slug: workspace.slug,
    token: workspace.access.token
  });
  assert.equal(resolved.id, workspace.id);

  const deleted = await service.deleteWorkspaceToken({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });
  assert.equal(deleted.access.tokenStatus, "deleted");
  await assert.rejects(
    service.resolveWorkspaceAccess({ slug: workspace.slug, token: workspace.access.token }),
    /workspace_token_inactive/
  );

  const reset = await service.resetWorkspaceToken({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });
  assert.equal(reset.access.tokenStatus, "active");
  assert.notEqual(reset.access.token, workspace.access.token);
  assert.match(reset.url, new RegExp(`token=${reset.access.token}$`));

  const resolvedAfterReset = await service.resolveWorkspaceAccess({
    slug: reset.slug,
    token: reset.access.token
  });
  assert.equal(resolvedAfterReset.id, workspace.id);

  const state = await service.getState("pi-alpha");
  assert.deepEqual(state.billingLedger.map((entry) => entry.type).filter((type) => type.startsWith("token_")), [
    "token_deleted",
    "token_reset"
  ]);
});
