import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { describe, it } from "node:test";

import {
  canUseOperatorControls,
  commercialLoginMethods,
  commercialUserRoleOptions,
  consoleMenuItems,
  eventRows,
  identityUserRows,
  landingFeatureCards,
  packageHoldPreview,
  publicNavigationItems,
  unauthenticatedViewForPath,
  visibleConsoleMenuItems,
  workspaceActionState,
  workspaceStatus,
  workspaceTableRows
} from "../../src/console-model.js";

const runningWorkspace = {
  id: "ws-alpha",
  name: "Grant Lab",
  packageId: "basic",
  state: "running",
  url: "https://workspace.medopl.cn/w/ws-alpha?token=share_x",
  provider: "tencent-tke",
  server: { spec: "2c4g", status: "running", billingStatus: "active" },
  disk: { sizeGb: 10, status: "attached_retained", billingStatus: "active" },
  docker: { image: "registry.example.com/opl/one-person-lab-app:latest", status: "running" },
  access: { tokenStatus: "active" }
};

describe("OPL Console view model", () => {
  it("uses a commercial public funnel with Home, Login, then Console", () => {
    assert.deepEqual(publicNavigationItems, [
      { path: "/", label: "Home" },
      { path: "/login", label: "Login" }
    ]);
    assert.equal(unauthenticatedViewForPath("/"), "home");
    assert.equal(unauthenticatedViewForPath("/login"), "login");
    assert.equal(unauthenticatedViewForPath("/anything-else"), "home");
  });

  it("keeps public login to one commercial email/password method", () => {
    assert.deepEqual(commercialLoginMethods, [
      { key: "email", label: "Email Login" }
    ]);
    assert.equal(commercialLoginMethods.some((method) => /invite|operator/i.test(method.label)), false);
  });

  it("keeps commercial user creation scoped to PI accounts only", () => {
    assert.deepEqual(commercialUserRoleOptions, [
      { label: "PI", value: "pi" }
    ]);
    assert.equal(commercialUserRoleOptions.some((option) => /operator/i.test(option.label)), false);
  });

  it("keeps the commercial UI out of accept-invite onboarding copy", async () => {
    const source = await readFile(new URL("../../src/main.jsx", import.meta.url), "utf8");

    assert.equal(/Accept Invite|Invite user|Create invite|Invite token/i.test(source), false);
  });

  it("positions OPL Cloud with product benefits rather than raw infrastructure", () => {
    const titles = landingFeatureCards.map((card) => card.title);
    assert.deepEqual(titles, [
      "Workspace distribution",
      "Prepaid billing control",
      "Managed runtime operations"
    ]);
    assert.equal(JSON.stringify(landingFeatureCards).match(/kubernetes|pod|pvc|ingress|deployment/i), null);
  });

  it("keeps navigation product-facing instead of exposing raw Kubernetes primitives", () => {
    const labels = consoleMenuItems.map((item) => item.label);
    assert.deepEqual(labels, [
      "Overview",
      "Account",
      "Workspaces",
      "Create Workspace",
      "Billing",
      "Alerts",
      "Audit",
      "Users",
      "Runtime"
    ]);
    assert.equal(labels.some((label) => /deployment|pod|pvc|ingress|service/i.test(label)), false);
  });

  it("keeps operator-only users and runtime controls out of the PI console", () => {
    const piLabels = visibleConsoleMenuItems({ role: "pi" }).map((item) => item.label);
    const operatorLabels = visibleConsoleMenuItems({ role: "operator" }).map((item) => item.label);

    assert.equal(piLabels.includes("Users"), false);
    assert.equal(piLabels.includes("Runtime"), false);
    assert.equal(operatorLabels.includes("Users"), true);
    assert.equal(operatorLabels.includes("Runtime"), true);
    assert.equal(canUseOperatorControls({ role: "pi" }), false);
    assert.equal(canUseOperatorControls({ role: "operator" }), true);
  });

  it("maps workspaces into table rows with package, URL, compute, storage, and billing state", () => {
    assert.deepEqual(workspaceTableRows([runningWorkspace]), [
      {
        key: "ws-alpha",
        id: "ws-alpha",
        name: "Grant Lab",
        packageId: "basic",
        status: "Running",
        statusTone: "success",
        url: "https://workspace.medopl.cn/w/ws-alpha?token=share_x",
        compute: "2c4g / running",
        storage: "10GB / attached_retained",
        computeBilling: "active",
        storageBilling: "active",
        tokenStatus: "active"
      }
    ]);
  });

  it("previews the seven-day package hold using priced compute and storage rates", () => {
    const hold = packageHoldPreview({
      id: "basic",
      diskGb: 10,
      price: {
        computeHourly: 1.2,
        storageGbMonth: 0.24
      }
    }, { prepaidHoldDays: 7 });

    assert.deepEqual(hold, {
      holdDays: 7,
      compute: 201.6,
      storage: 0.56,
      total: 202.16
    });
  });

  it("disables access and lifecycle actions after storage is destroyed", () => {
    const destroyed = {
      ...runningWorkspace,
      state: "destroyed",
      server: { ...runningWorkspace.server, status: "destroyed", billingStatus: "stopped" },
      disk: { ...runningWorkspace.disk, status: "destroyed", billingStatus: "stopped" },
      access: { tokenStatus: "unavailable" }
    };
    assert.deepEqual(workspaceStatus(destroyed), {
      label: "Destroyed",
      tone: "default"
    });
    assert.deepEqual(workspaceActionState(destroyed), {
      canCopyUrl: false,
      canResetToken: false,
      canDeleteToken: false,
      canStopCompute: false,
      canRestartCompute: false,
      canDestroyCompute: false,
      canDestroyStorage: false
    });
  });

  it("maps identity users without leaking password hashes", () => {
    assert.deepEqual(identityUserRows([{
      id: "user-pi",
      email: "pi@example.com",
      accountId: "pi-alpha",
      tenantId: "tenant-alpha",
      displayName: "Grant Lab PI",
      role: "pi",
      status: "disabled",
      passwordHash: "scrypt:secret",
      createdAt: "2026-07-01T00:00:00.000Z",
      updatedAt: "2026-07-02T00:00:00.000Z"
    }]), [{
      key: "pi@example.com",
      id: "user-pi",
      email: "pi@example.com",
      accountId: "pi-alpha",
      tenantId: "tenant-alpha",
      displayName: "Grant Lab PI",
      role: "pi",
      status: "disabled",
      statusTone: "error",
      createdAt: "2026-07-01T00:00:00.000Z",
      updatedAt: "2026-07-02T00:00:00.000Z"
    }]);
  });

  it("keeps account identity visible in audit rows", () => {
    assert.deepEqual(eventRows([{
      id: "audit-1",
      accountId: "pi-alpha",
      workspaceId: "",
      type: "identity.user_disabled",
      sourceEventId: "manual_operator_disable",
      createdAt: "2026-07-02T00:00:00.000Z"
    }]), [{
      key: "audit-1",
      id: "audit-1",
      type: "identity.user_disabled",
      accountId: "pi-alpha",
      workspaceId: "",
      sourceEventId: "manual_operator_disable",
      severity: "",
      message: "",
      amount: undefined,
      createdAt: "2026-07-02T00:00:00.000Z"
    }]);
  });
});
