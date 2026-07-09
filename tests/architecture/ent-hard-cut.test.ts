import assert from "node:assert/strict";
import { access, readdir, readFile } from "node:fs/promises";
import { join, relative } from "node:path";
import test from "node:test";

const repoRoot = new URL("../../", import.meta.url);

async function exists(relativePath) {
  try {
    await access(new URL(relativePath, repoRoot));
    return true;
  } catch (error) {
    if (error?.code === "ENOENT") return false;
    throw error;
  }
}

async function files(dir, pattern = /.*/) {
  const current = new URL(dir.endsWith("/") ? dir : `${dir}/`, repoRoot);
  let entries = [];
  try {
    entries = await readdir(current, { withFileTypes: true });
  } catch (error) {
    if (error?.code === "ENOENT") return [];
    throw error;
  }
  const out = [];
  for (const entry of entries) {
    const child = join(current.pathname, entry.name);
    if (entry.isDirectory()) {
      out.push(...await files(relative(repoRoot.pathname, child), pattern));
    } else if (pattern.test(entry.name)) {
      out.push(relative(repoRoot.pathname, child));
    }
  }
  return out;
}

async function source(relativePath) {
  return readFile(new URL(relativePath, repoRoot), "utf8");
}

test("Go services use Ent schema and migrations for production persistence", async () => {
  for (const service of ["control-plane", "ledger", "fabric"]) {
    assert.equal(await exists(`services/${service}/ent/schema`), true, `${service} must define Ent schemas`);
    assert.equal(await exists(`services/${service}/migrations`), true, `${service} must define versioned migrations`);
    const schemas = await files(`services/${service}/ent/schema`, /\.go$/);
    const migrations = await files(`services/${service}/migrations`, /\.sql$/);
    assert.ok(schemas.length > 0, `${service} must have Ent schema files`);
    assert.ok(migrations.length > 0, `${service} must have SQL migration files generated from schema`);
  }
});

test("Control Plane Ent model includes current facts and archive facts", async () => {
  const requiredSchemas = [
    "account.go",
    "user.go",
    "membership.go",
    "session.go",
    "auth_attempt.go",
    "compute_allocation.go",
    "storage_volume.go",
    "storage_attachment.go",
    "workspace.go",
    "wallet_projection.go",
    "ledger_projection.go",
    "wallet_transaction_projection.go",
    "manual_topup_projection.go",
    "billing_reconciliation.go",
    "runtime_operation.go",
    "admin_audit_event.go",
    "support_ticket_mapping.go",
    "production_e2e_record.go",
    "archive_job.go",
    "archived_compute_allocation.go",
    "archived_storage_volume.go",
    "archived_storage_attachment.go",
    "archived_workspace.go",
    "archived_admin_audit_event.go"
  ];
  for (const schema of requiredSchemas) {
    assert.equal(await exists(`services/control-plane/ent/schema/${schema}`), true, `missing Control Plane Ent schema ${schema}`);
  }
});

test("Ledger and Fabric Ent models cover money and cloud-operation facts", async () => {
  for (const schema of [
    "wallet.go",
    "ledger_entry.go",
    "wallet_transaction.go",
    "manual_topup.go",
    "hold.go",
    "hold_release.go",
    "evidence_receipt.go",
    "resource_settlement.go",
    "reconciliation_report.go",
    "idempotency_key.go"
  ]) {
    assert.equal(await exists(`services/ledger/ent/schema/${schema}`), true, `missing Ledger Ent schema ${schema}`);
  }
  for (const schema of ["fabric_operation.go", "workspace_runtime_access.go"]) {
    assert.equal(await exists(`services/fabric/ent/schema/${schema}`), true, `missing Fabric Ent schema ${schema}`);
  }
});

test("production data path does not keep hand-written SQL fact stores after Ent hard cut", async () => {
  const goFiles = [
    ...await files("services/control-plane", /\.go$/),
    ...await files("services/ledger", /\.go$/),
    ...await files("services/fabric", /\.go$/)
  ].filter((file) => !file.includes("/ent/") && !file.endsWith("_test.go"));

  const forbidden = [
    "type factRow",
    "type factTable",
    "type controlPlaneFacts",
    "FactStore interface",
    "postgresFactStore",
    "fileFactStore",
    "OPL_CONTROL_PLANE_FACTS_FILE",
    "const postgresSchema =",
    "const postgresOperationSchema =",
    "CREATE TABLE IF NOT EXISTS"
  ];

  for (const file of goFiles) {
    const text = await source(file);
    for (const marker of forbidden) {
      assert.equal(text.includes(marker), false, `${file} must not keep retired production persistence marker ${marker}`);
    }
  }
});
