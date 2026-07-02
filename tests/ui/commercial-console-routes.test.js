import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const routesPath = new URL("../../packages/console/ui/consoleRoutes.js", import.meta.url);
const consolePagePath = new URL("../../packages/console/ui/pages/ConsolePage.jsx", import.meta.url);

async function source(path) {
  return readFile(path, "utf8");
}

test("commercial Console route contract covers public, auth, console, support, and admin routes", async () => {
  const routes = await source(routesPath);

  for (const path of [
    "/",
    "/pricing",
    "/docs",
    "/status",
    "/login",
    "/register",
    "/invite/accept",
    "/email/verify",
    "/forgot-password",
    "/reset-password",
    "/console/overview",
    "/console/workspaces",
    "/console/workspaces/new",
    "/console/workspaces/:id/access",
    "/console/gateway",
    "/console/billing/wallet",
    "/console/account/lab",
    "/console/support",
    "/console/support/new",
    "/console/support/:id",
    "/console/resources/connectors",
    "/console/approvals",
    "/console/receipts",
    "/admin/overview",
    "/admin/users/:id/wallet",
    "/admin/governance/policies",
    "/admin/billing/topups",
    "/admin/fabric/connectors",
    "/admin/ledger/events",
    "/admin/runtime/readiness",
    "/admin/support",
    "/403",
    "/404",
    "/500"
  ]) {
    assert.match(routes, new RegExp(`path:\\s*["']${path.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}["']`));
  }

  assert.match(routes, /requiresAuth:\s*true/);
  assert.match(routes, /requiresAdmin:\s*true/);
  assert.match(routes, /hiddenInMenu:\s*true/);
  assert.match(routes, /featureGate:\s*["']registration["']/);
});

test("Lab Owner menu stays commercial and hides operator internals", async () => {
  const routes = await source(routesPath);

  for (const label of [
    "Overview",
    "Workspaces",
    "Gateway",
    "Billing",
    "Account & Lab",
    "Support",
    "Alerts"
  ]) {
    assert.match(routes, new RegExp(`label:\\s*["']${label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}["']`));
  }

  for (const forbidden of [
    "Manual settlement",
    "Request fingerprint",
    "Dedup rows",
    "Production readiness",
    "TKE/K8s",
    "Raw Ledger events"
  ]) {
    assert.doesNotMatch(routes, new RegExp(`role:\\s*["']owner["'][\\s\\S]*${forbidden}`));
  }
});

test("commercial UI source keeps Lab Owner away from low-level billing and runtime evidence", async () => {
  const page = await source(consolePagePath);

  assert.doesNotMatch(page, /结算 1 小时/);
  assert.doesNotMatch(page, /requestUsageDedup/);
  assert.doesNotMatch(page, /requestFingerprint/);
  assert.doesNotMatch(page, /production readiness/i);
  assert.match(page, /Support/);
  assert.match(page, /Workspace URL/);
  assert.match(page, /7-day hold/);
});
