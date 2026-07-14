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
    "organization.go",
    "user.go",
    "membership.go",
    "session.go",
    "auth_attempt.go",
    "compute_allocation.go",
    "storage_volume.go",
    "storage_attachment.go",
    "workspace.go",
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

test("Ledger and Fabric Ent models cover evidence and cloud-operation facts", async () => {
  for (const schema of [
    "evidence_receipt.go",
    "reconciliation_report.go",
    "review_policy.go",
    "idempotency_key.go"
  ]) {
    assert.equal(await exists(`services/ledger/ent/schema/${schema}`), true, `missing Ledger Ent schema ${schema}`);
  }
	for (const schema of ["wallet.go", "ledger_entry.go", "wallet_transaction.go", "manual_topup.go", "hold.go", "hold_release.go", "resource_settlement.go"]) {
		assert.equal(await exists(`services/ledger/ent/schema/${schema}`), false, `retired Ledger Ent schema ${schema} must be deleted`);
	}
	for (const schema of ["fabric_operation.go", "connector.go", "environment_template.go"]) {
    assert.equal(await exists(`services/fabric/ent/schema/${schema}`), true, `missing Fabric Ent schema ${schema}`);
  }

  assert.equal(await exists("services/fabric/ent/schema/workspace_runtime_access.go"), false, "Fabric must not model runtime credentials as database state");
});

test("Workspace password authority is Kubernetes Secret only", async () => {
  const fabricFiles = await files("services/fabric/ent", /\.go$/);
  for (const file of fabricFiles) {
    assert.equal((await source(file)).toLowerCase().includes("workspaceruntimeaccess"), false, `${file} retains the retired runtime-access entity`);
  }

  const workspaceSchema = await source("services/control-plane/ent/schema/shared.go");
  assert.equal(workspaceSchema.includes('field.String("access_password")'), false, "Control Plane must not model plaintext Workspace passwords");
  for (const file of await files("services/control-plane/ent", /\.go$/)) {
    assert.equal((await source(file)).includes("AccessPassword"), false, `${file} retains generated Workspace password persistence`);
  }

  const fabricMigrations = (await Promise.all([
    ...await files("services/fabric/migrations", /\.sql$/),
    ...await files("services/fabric/internal/fabric/ent_migrations", /\.sql$/)
  ].map(source))).join("\n");
  const controlPlaneMigrations = (await Promise.all([
    ...await files("services/control-plane/migrations", /\.sql$/),
    ...await files("services/control-plane/internal/server/ent_migrations", /\.sql$/)
  ].map(source))).join("\n");
  assert.match(fabricMigrations, /DROP TABLE IF EXISTS fabric_workspace_runtime_access/, "Fabric needs a formal hard-cut migration");
  assert.match(controlPlaneMigrations, /DROP COLUMN IF EXISTS access_password/, "Control Plane needs a formal hard-cut migration");
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

test("Control Plane schema and migrations do not inherit a generic wide fact table", async () => {
  for (const file of await files("services/control-plane/ent/schema", /\.go$/)) {
    const text = await source(file);
    assert.equal(text.includes("commonFactFields()"), false, `${file} must use business-specific Ent fields`);
  }

  const sharedSchema = await source("services/control-plane/ent/schema/shared.go");
  assert.match(sharedSchema, /return table\("control_plane_accounts"\)/, "Control Plane Ent schemas must target control_plane_* tables");

  for (const migration of [
    ...await files("services/control-plane/migrations", /\.sql$/),
    ...await files("services/control-plane/internal/server/ent_migrations", /\.sql$/)
  ]) {
    const text = await source(migration);
    assert.equal(text.includes("LIKE control_plane_accounts"), false, `${migration} must define tables explicitly`);
    if (!text.includes("CREATE TABLE")) continue;
    assert.match(text, /control_plane_organizations/, `${migration} must include the organization facts read by Control Plane`);
    const accountsTable = text.split("\n").find((line) => line.includes("CREATE TABLE IF NOT EXISTS control_plane_accounts"));
    const usersTable = text.split("\n").find((line) => line.includes("CREATE TABLE IF NOT EXISTS control_plane_users"));
    assert.ok(accountsTable, `${migration} must define control_plane_accounts`);
    assert.ok(usersTable, `${migration} must define control_plane_users`);
    assert.equal(accountsTable.includes("password_hash"), false, `${migration} accounts must not inherit user columns`);
    assert.equal(accountsTable.includes("provider_resource_id"), false, `${migration} accounts must not inherit resource columns`);
    assert.equal(usersTable.includes("provider_resource_id"), false, `${migration} users must not inherit resource columns`);
  }
});

test("Control Plane production store does not expose generic state row tables", async () => {
  const storeSource = await source("services/control-plane/internal/server/ent_state_store.go");
  for (const marker of [
    "type stateRow",
    "type stateTable",
    "postgresStateColumns",
    "postgresFactTables",
    "postgresFactEventTables",
    "\"database/sql\"",
    "replaceStateTable",
    "replaceStateEvents",
    "replaceSingleton",
    "controlPlaneRecordColumns",
    "insertFactSQL",
    "selectFactSQL"
  ]) {
    assert.equal(storeSource.includes(marker), false, `Control Plane store must not keep ${marker}`);
  }
});
