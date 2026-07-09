# Ent Hard-Cut Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hand-written SQL fact stores in Control Plane, Ledger, and Fabric with Ent schema + migrations, add first-class archive tables, and keep the frontend displaying only backend DTOs.

**Architecture:** Each service owns its Ent schema, migrations, repository/store layer, and business service. Control Plane keeps aggregating DTOs for Console, but the aggregation reads typed stores instead of a generic fact store. Ledger remains the money source of truth; Fabric remains the cloud-operation source of truth.

**Tech Stack:** Go 1.22, Ent, PostgreSQL, existing net/http services, React/Vite Console UI, Node contract tests.

---

## Phase 1: Architecture Gates

- [x] Add tests that require Ent schema and migration directories in `services/control-plane`, `services/ledger`, and `services/fabric`.
- [x] Add tests that forbid product-path `fact_store`, `controlPlaneFacts`, `postgresFactStore`, and service-local `CREATE TABLE` schemas after the hard cut.
- [x] Add tests that require archive schema coverage for Control Plane resource facts and audit facts.
- [x] Run `npm test` and confirm the new tests fail before implementation.

## Phase 2: Ent Schema And Migrations

- [x] Add Ent dependencies and generation config to each Go service.
- [x] Create Control Plane schemas for account, user, membership, session, auth attempt, compute allocation, storage volume, storage attachment, workspace, billing projections, runtime operation, admin audit event, support ticket mapping, production E2E record, archive job, and archived resource/audit rows.
- [x] Create Ledger schemas for wallet, ledger entry, wallet transaction, manual topup, hold, hold release, evidence receipt, resource settlement, reconciliation report, and idempotency key.
- [x] Create Fabric schemas for fabric operation and workspace runtime access.
- [x] Add versioned SQL migrations.
- [x] Generate Ent clients after the Ent generator dependencies are available.
- [x] Confirm schema output contains no product fact `payload JSONB`.

## Phase 3: Control Plane Ent Store

- [ ] Replace `FactStore` with typed Ent repositories.
- [x] Remove file fact store and `OPL_CONTROL_PLANE_FACTS_FILE`.
- [ ] Make `/api/state` and `/api/management/state` aggregate Ent facts plus Ledger/Fabric HTTP reads only.
- [ ] Delete old fact-store tests once Ent repository tests cover the behavior.
- [ ] Confirm frontend-facing DTOs still expose price, wallet, resources, URL, account, audit, and support facts from backend responses.

## Phase 4: Ledger Ent Store

- [x] Replace `PostgresStore` hand-written SQL with an Ent-backed store implementing the existing `ledger.Store` interface.
- [x] Preserve idempotency conflict behavior for topup, hold, release, evidence, settlement, and reconciliation.
- [x] Preserve wallet math: topup increases balance, hold increases frozen, release reduces frozen without reducing balance, settlement reduces balance and increases spent.
- [x] Keep `resource_settlements.price_snapshot_json` as a controlled audit snapshot, not a frontend fact source.

## Phase 5: Fabric Ent Store

- [ ] Replace `PostgresOperationStore` hand-written SQL with an Ent-backed operation/runtime access store.
- [ ] Preserve resource replay from fabric operations.
- [ ] Preserve controlled runtime credential API behavior.
- [ ] Keep redacted provider payload as an audit evidence snapshot.

## Phase 6: Archive Domain

- [ ] Add archive tables for destroyed compute allocations, storage volumes, storage attachments, workspaces, and admin audit events.
- [ ] Add `ArchiveService` and retention worker for terminal Control Plane facts.
- [ ] Exclude archived resources from customer current-state pages.
- [ ] Add admin archive API and frontend API client surface.
- [ ] Do not archive or delete Ledger accounting facts in this phase.

## Phase 7: Periodic Settlement Ent Path

- [ ] Make the periodic settlement worker scan Ent-backed active compute and storage resources.
- [ ] Update last-settled metadata after successful Ledger settlement.
- [ ] Keep stable settlement idempotency keys.
- [ ] Prove repeated worker runs do not double-charge.

## Phase 8: Hard Cut Cleanup

- [ ] Delete old compatibility paths, file persistence, generic fact store, and demo/fallback data.
- [ ] Run `rg "fact_store|postgresFactStore|fileFactStore|controlPlaneFacts|payload JSONB|demo|fallback|compat|legacy"` and leave only tests/history docs that intentionally forbid retired paths.
- [ ] Run `go test ./...` in all three services.
- [ ] Run `npm test`, `npm run typecheck`, and `sentrux check .`.
- [ ] Commit and push the feature branch. Do not rollout.
