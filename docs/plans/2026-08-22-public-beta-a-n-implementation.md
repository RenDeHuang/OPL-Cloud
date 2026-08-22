# Public Beta A-N Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Deliver public registration, administrator funding, controlled Workspace purchase, complete lifecycle recovery, isolated restore verification, stable operational signals, and exact-byte Candidate-to-Release promotion.

**Architecture:** Keep Account/Access, Workspace lifecycle, Fabric resources, Ledger evidence, and distribution in their existing owners. Persist intent before uncertain work, converge through typed owner readback and CAS, and serialize shared schema, contracts, server wiring, canonical `main`, Candidate construction, Instance mutations, and Release publication.

**Tech Stack:** Go 1.24 services, Ent and PostgreSQL, typed HTTP clients, React/TypeScript Console, Node test runner, Docker Compose, GitHub Actions, GHCR, and GitHub Releases.

---

## Fixed Decisions And Branches

- Baseline: `main@95843e5f8f19b0c7e72f288c022a18d4338628d4`, tree `f685f0af70d169a4ea50df5a2c956c320e039b9b`.
- H is trigger-gated verification only. I is regression verification only.
- J covers Registration, Delete, and Reset recovery; it does not introduce a global operation framework.
- K/L/M/N implement Cloud-owned behavior and receipt admission. Instance execution receipts remain separately owned.
- Public Registration is explicit `full_cloud_customer` admission with `workspacePurchaseEnabled=true` and an authoritative zero-balance readback. A Sub2API identity alone never becomes a Cloud Account.
- Background Delete uses a typed Sub2API administrator exact-Key read/delete capability. Delegated Session credentials remain memory-only and are never persisted.
- Control Plane migration `202608220001_public_registration_limits.sql` is reserved to M. Registration persists a strict typed canonical JSON result in the existing RuntimeOperation table; A, C Delete, D, and C Renewal add no schema migration.
- The Schema/Contract writer serially owns every new or revised machine contract and both consumers.
- `table_store.go` and `memory_table_store_test.go` have one serialized writer. D and C-Renewal may develop reducers, owner adapters, PostgreSQL methods, and dedicated tests in parallel, but their shared interface/test wiring is joined only after both method contracts are fixed.
- J/L use existing RuntimeOperation and audit persistence. If implementation evidence forces another schema change, K waits and Schema Freeze moves after that migration.

Use these initial worktrees after this plan is merged:

```text
codex/pb-a-fabric-delete
codex/pb-d-public-registration
codex/pb-c-renewal-reactivation
```

Use one narrow PR per alias or cohesive dependency. Every PR follows
`.github/PULL_REQUEST_TEMPLATE.md`, names the `PB-*` ID, exact write set,
overlap, SSOT reconciliation, and exact focused commands, and uses an issue
link/close statement in the same style as PR #365 / issue #356.

The first parallel pass must not edit `services/control-plane/internal/server/table_store.go`
or `services/control-plane/internal/server/memory_table_store_test.go` in both
worktrees. The coordinator reserves those files, reconciles both narrow store
contracts in one integration commit, and reruns both focused suites before
either capability is declared complete.

## Verification Schedule

Focused acceptance, not a repository-wide command, closes each work package.
Every implementation and review loop runs only the affected domain,
application, persistence, UI, or contract tests. A persistence owner runs its
exact PostgreSQL tests against a task-owned isolated database and records zero
skips; it does not use the whole PostgreSQL suite as a substitute.

Each PR runs `npm run verify:local` once after its focused evidence is green and
the diff is frozen. For this A-N program, the persistence/cross-module
integration checkpoints named by `docs/roadmap.md` are the Wave 1 fresh-main
join, the Wave 2 fresh-main join, and, only when its identity changed, the
Pre-Candidate gate. The Wave coordinator alone runs
`npm run verify:local:full`; capability implementers and reviewers do not run or
request it per task.

A reviewer may require another focused scenario for a concrete current caller,
observed failure, authority conflict, or reachable invariant violation. It may
not request a full run, a defensive test catalog, or unrelated refactoring as
capability acceptance.

Completion states remain distinct:

```text
module_complete   = focused + exact PostgreSQL where applicable + verify:local
wave_integrated   = fresh-main verify:local:full bound to exact SHA/tree
candidate_ready   = frozen Candidate SHA/tree equals the latest integrated gate
public_beta_ready = Candidate and all required Instance/Release receipts agree
```

## Wave 0: Freeze And Baseline

### Task 1: Land the accepted design and test baseline

**Files:**
- Modify: `docs/architecture.md`
- Modify: `docs/implementation-architecture.md`
- Modify: `docs/product/console-workspace-v1.md`
- Modify: `docs/roadmap.md`
- Modify: `docs/runtime/production-runbook.md`
- Modify: `docs/status.md`
- Create: `docs/plans/2026-08-22-public-beta-a-n-design.md`
- Create: `docs/plans/2026-08-22-public-beta-a-n-execution-dag.mmd`
- Create: `docs/plans/2026-08-22-public-beta-a-n-implementation.md`

**Steps:**
1. Confirm the four accepted user decisions and exact Candidate bridge rule are represented once in their canonical owners.
2. Run A focused Fabric observation tests.
3. Run B memory and exact PostgreSQL Receipt CAS tests against one isolated database.
4. Run H same-pool, different-pool, and expired-lease focused tests without changing capacity policy.
5. Run I catalog, quote, Basic/Pro admission, exact-balance, and Console focused tests.
6. Run `npm run verify:local`; do not create a Wave full gate for a documentation-only baseline.
7. Run `git diff --check`, commit, push, open the roadmap PR, wait for `validate`, merge, and read back remote `main` SHA/tree.

## Wave 1 Lane 1: Resource Lifecycle

### Task 2: A - converge exact Tencent Gateway Secret and PV/PVC residue

**Files:**
- Modify: `services/fabric/internal/fabric/types.go`
- Modify: `services/fabric/internal/fabric/provider_port.go`
- Modify: `services/fabric/internal/fabric/workspace_runtime.go`
- Modify: `services/fabric/internal/fabric/tencent_provider_runtime.go`
- Modify: `services/fabric/internal/fabric/tencent_provider_storage.go`
- Modify: `services/fabric/internal/http/server.go`
- Modify: `services/control-plane/internal/clients/fabric.go`
- Modify: `services/control-plane/internal/controlplane/workspace_delete.go`
- Modify: `services/control-plane/internal/server/workspace_delete.go`
- Create: `packages/contracts/opl-cloud-fabric-workspace-delete-contract.json`
- Modify: `packages/contracts/README.md`
- Test: `services/fabric/internal/fabric/tencent_provider_test.go`
- Test: `services/fabric/internal/fabric/workspace_runtime_delete_observation_test.go`
- Test: `services/fabric/internal/fabric/service_test.go`
- Test: `services/fabric/internal/fabric/postgres_runtime_integration_test.go`
- Test: `services/fabric/internal/http/server_test.go`
- Test: `services/control-plane/internal/clients/fabric_test.go`
- Test: `services/control-plane/internal/server/workspace_delete_test.go`
- Test: `tests/contracts/*fabric*delete*.test.ts`

**Steps:**
1. Add failing tests for Gateway-only residue, Runtime-absent/Secret-present, exact replay, identity drift, duplicate identity, and owner read error. Assert zero unrelated mutation on unknown/conflict.
2. Add failing PV/PVC matrix tests for both present, PVC-only, PV-only, both absent, foreign labels, handle drift, `volumeName` drift, malformed lists, and duplicates.
3. Define one strict contract for exact persisted delete identity and typed residue observation. Reject unknown JSON fields and identity drift in both consumers.
4. Implement independent Gateway Secret GET/classify/delete/readback that does not depend on a surviving Deployment.
5. Implement independent PV and PVC classification and deletion from persisted `pvName`, `pvcName`, CBS identity, region, and ownership labels. Delete only the exact residue proven owned.
6. Preserve provider attempt identity across response loss and require final `absent` readback for every owned component.
7. Run:

```bash
cd services/fabric
go test -count=1 ./internal/fabric -run 'Test(ObserveWorkspaceRuntimeDelete|WorkspaceRuntimeDeleteResiduals|DestroyWorkspaceRuntime|TencentProviderDestroyStorage|TencentGatewaySecret)'
go test -count=1 ./internal/http
cd ../control-plane
go test -count=1 ./internal/clients ./internal/server -run 'Test(FabricHTTPClient.*Delete|WorkspaceDelete.*Residual)'
cd ../..
node --test 'tests/contracts/*fabric*delete*.test.ts'
npm run verify:local
```

8. Commit, self-review, receive spec review then quality review, push, open the A PR, pass CI, merge, and read back `main`.

### Task 3: C Delete - add a bounded service-authorized reconciler

**Files:**
- Modify: `services/control-plane/internal/clients/sub2api.go`
- Modify: `services/control-plane/internal/clients/sub2api_test.go`
- Modify: `services/control-plane/internal/server/workspace_delete.go`
- Create: `services/control-plane/internal/server/workspace_delete_reconciler.go`
- Create: `services/control-plane/internal/server/workspace_delete_worker.go`
- Create: `services/control-plane/internal/server/workspace_delete_worker_test.go`
- Modify: `services/control-plane/internal/server/server.go`
- Modify: `services/control-plane/internal/server/workspace_delete_test.go`
- Modify: `services/control-plane/internal/server/ent_state_store_test.go`

**Steps:**
1. Add a typed Sub2API administrator exact-Key lookup/delete port. Bind account, remote user, Workspace, Key ID, and deterministic operation identity; reject cardinality or owner drift before delete.
2. Refactor the existing v2 Delete operation into typed command/result/reducer functions while explicitly decoding current persisted rows.
3. Add transition tests that reject unknown fields, skipped phases, stale lease/CAS, identity drift, and terminal reopening.
4. Implement one-step reconciliation: claim/lease, authoritative owner read, permitted mutation, persisted result, projection, Receipt, and final readback.
5. Add startup plus ticker scanning of non-terminal `workspace.delete.v2` operations, with fixed page and per-tick budgets. Never retry `unknown` or `conflict` mutation results without a decisive readback.
6. Prove two workers have one CAS winner and restart completes without duplicate Key deletion, Fabric mutation, Workspace projection, or Receipt.
7. Prove `manual_review` appears with a bounded allowed action and does not block unrelated work.
8. Run the focused Delete reducer/worker tests and exact PostgreSQL Delete restart tests with zero skips, then run `npm run verify:local` once.
9. Commit/review/push/PR/merge/readback as a separate C-Delete change.

## Wave 1 Lane 2: Customer Commerce

### Task 4: D - persist public Registration and expose API/Console

**Files:**
- Create: `services/control-plane/internal/server/registration.go`
- Create: `services/control-plane/internal/server/registration_test.go`
- Create: `services/control-plane/internal/server/registration_postgres_test.go`
- Modify: `services/control-plane/internal/server/routes_auth.go`
- Modify: `services/control-plane/internal/server/auth_accounts.go`
- Modify: `services/control-plane/internal/server/ent_state_store_identity.go`
- Modify: `services/control-plane/internal/server/routes_admin.go`
- Create: `services/control-plane/internal/server/registration_store_test.go`
- Modify: `services/control-plane/internal/server/operator_projection_test.go`
- Modify: `services/control-plane/internal/server/identity_hard_cut_test.go`
- Modify: `services/control-plane/internal/server/provisioned_account_test.go`
- Modify: `services/control-plane/internal/server/server_test.go`
- Modify: `services/control-plane/internal/clients/sub2api_identity_test.go`
- Modify: `apps/console-ui/src/api/dtos.ts`
- Modify: `apps/console-ui/src/api/auth-api.ts`
- Modify: `apps/console-ui/src/app/use-console-controller.ts`
- Modify: `apps/console-ui/src/app/console-controller-types.ts`
- Modify: `apps/console-ui/src/app/console-router.ts`
- Modify: `apps/console-ui/src/App.tsx`
- Modify: `apps/console-ui/src/pages/PublicPages.tsx`
- Modify: `apps/console-ui/src/styles.css`
- Create: `tests/ui/public-registration.test.ts`

**Registration operation:**

```text
intent_persisted
  -> remote_identity_confirmed
  -> zero_balance_confirmed
  -> local_identity_persisted
  -> audit_persisted
  -> complete
```

The operation is keyed by canonical email, not by an arbitrary request key.
`requestHash` covers only canonical non-secret input. Persist no raw password or
password hash. Its typed state is canonical JSON in the existing
RuntimeOperation `result`; current and supported historical forms are decoded
strictly rather than promoted to new Ent columns. A public client never reads
an operation by email. Before remote identity confirmation, and after any lost
response, the client resubmits the same `POST` and password; the server
authenticates that identity before returning or advancing the same operation.
After remote identity confirmation, internal/operator recovery uses persisted
opaque remote identity and owner GET/readback only.

**Steps:**
1. Write decoder/reducer tests for exact fields, legal phases/statuses, same-email replay, request drift, and fail-closed current-row decoding.
2. Add Registration-specific Claim/Persist CAS to memory and PostgreSQL stores; do not use process locks as the correctness boundary.
3. Persist intent before Sub2API create/auth. On response loss, converge by canonical email, authentication, exact User identity, and Balance readback.
4. Require active authoritative balance `usdMicros == 0`; a pre-existing non-zero Gateway wallet becomes operator-assisted/manual review and is never cleared.
5. Atomically create one Account/User with `workspacePurchaseEnabled=true`, then write one deterministic audit event. Replays return the same operation and identity.
6. Add only `POST /api/auth/register` to the pre-session surface. Enforce exact JSON, same-origin, content type, request size, non-enumerating errors, authenticated same-POST replay, and no-store responses. Do not add a public email-keyed Registration GET.
7. Keep separate typed admission policy: public Registration is always `full_cloud_customer`, zero-balance-gated, and eligible; administrator `gateway_only` preserves any existing wallet balance and remains ineligible. The two paths share only identity convergence and atomic local-mapping primitives, not a public policy reducer.
8. Add `/register` Console route, controlled form, pending/retry/conflict UI, and post-completion login without exposing internal operation data.
9. Prove concurrent same-email and response loss across PostgreSQL restart create one Registration, Account, User, Sub2API identity, audit event, and zero balance.
10. Run:

```bash
cd services/control-plane
go test -count=1 ./internal/server -run '^TestRegistration(Operation|Reducer|Decoder|Store)'
go test -count=1 ./internal/server -run '^TestPublicRegistration'
go test -count=1 ./internal/clients -run '^TestSub2APIIdentity'
cd ../..
node --test tests/ui/public-registration.test.ts tests/ui/console-model.test.ts tests/ui/console-browser-acceptance.test.ts
npm run typecheck
npm run build
npm run verify:local
```

Run `TestPostgresRegistration*` separately against the task-owned isolated
PostgreSQL database and require zero skips. Do not invoke the full PostgreSQL
suite for Registration acceptance.
11. Commit/review/push/PR/merge/readback.

### Task 5: E - prove register, insufficient balance, top-up, and purchase

**Files:**
- Create: `services/control-plane/internal/server/public_beta_commerce_test.go`
- Create or modify: `services/control-plane/internal/server/public_beta_commerce_postgres_test.go`
- Modify only if a proved defect exists in the owning D, wallet adjustment, admission, or Launch module.

**Steps:**
1. Register and login a new public customer; assert one admitted identity and zero wallet balance.
2. Submit Basic and Pro launches with zero/insufficient balance. Assert no Launch operation, debit, Fabric mutation/resource chain, or Ledger Receipt; authoritative read-only quote/provider facts remain permitted.
3. Administrator top up exactly the authoritative quote through the existing wallet adjustment operation.
4. Launch at exact balance and require one debit, one resource chain, one projected purchase Receipt, and a zero final balance.
5. Replay Registration, top-up, and Launch across server restart; require cardinality one for every irreversible fact.
6. Assert Catalog, Quote, DTO, Console, and server admission still agree for Basic/Pro and `balance >= quote`.
7. Run:

```bash
cd services/control-plane
go test -count=1 ./internal/server -run '^TestPublicBetaRegisterTopupPurchase'
cd ../..
node --test tests/ui/console-model.test.ts tests/ui/pricing-preview.test.ts
npm run verify:local
```

Require the exact PostgreSQL restart variant to run with zero skips, then commit/review/PR/merge.

### Task 6: M - close Cloud public application boundaries

**Files:**
- Create: `services/control-plane/migrations/202608220001_public_registration_limits.sql`
- Modify: `services/control-plane/migrations/migrations.go`
- Modify: `services/control-plane/migrations/migrations_test.go`
- Modify: `services/control-plane/internal/server/routes_auth.go`
- Modify: `services/control-plane/internal/server/registration.go`
- Create: `services/control-plane/internal/server/registration_security_test.go`
- Create: `services/control-plane/internal/server/account_data_exit.go`
- Create: `services/control-plane/internal/server/account_data_exit_test.go`
- Modify: `services/control-plane/internal/server/routes_admin.go`
- Modify: relevant Console account/operator DTO/controller/page files

**Steps:**
1. Wire `control_plane_auth_attempts` to a durable direct-peer plus canonical-email Registration limiter and add only the required query index. Do not trust forwarded client IPs.
2. Test non-enumeration, Origin, CSRF/session separation, JSON/body bounds, limiter restart/multi-instance behavior, account disable, Session revocation, tenant isolation, and no-store secret reads.
3. Implement operator-assisted data exit as a typed command. It must prove no active Workspace/Delete/Key/Session blockers before marking the User deleted, disabling new purchases, and revoking Sessions.
4. Preserve Account billing/Receipt custody and never delete or rewrite the Sub2API wallet.
5. Add Console projections/actions for operator recovery and data exit without exposing sensitive blocker details to another tenant.
6. Run:

```bash
cd services/control-plane
go test -count=1 ./migrations
go test -count=1 ./internal/server -run '^Test(RegistrationSecurity|PublicRegistrationRateLimit|AccountDataExit|AccountDisable|ConsoleTenantIsolation)'
cd ../..
node --test tests/ui/public-registration.test.ts tests/ui/console-browser-acceptance.test.ts
npm run typecheck
npm run build
npm run verify:local
```
7. Run only the exact limiter/migration PostgreSQL tests with zero skips; commit/review/PR/merge/readback. TLS, ingress, and real-client edge limiting remain Instance receipts.

## Wave 1 Lane 3: Renewal And Distribution Preparation

### Task 7: C Renewal - explicitly reactivate an expired Workspace

**Files:**
- Modify: `services/control-plane/internal/server/workspace_renewal.go`
- Modify: `services/control-plane/internal/server/routes_workspace.go`
- Modify: `services/control-plane/internal/server/ent_state_store_workspace.go`
- Create: `services/control-plane/internal/server/workspace_renewal_reactivation_store_test.go`
- Modify: `services/control-plane/internal/server/workspace_renewal_test.go`
- Modify: `services/control-plane/internal/server/ent_state_store_test.go`

**Steps:**
1. Add a typed owner reactivation command bound to account, Workspace, prior `paidThrough`, original operation, expiry Receipt, actor, and stable idempotency key.
2. Reject unknown fields, a non-terminal expiry, any prior charge/refund/provider mutation, owner/resource drift, and a concurrent Delete.
3. Atomically persist command, audit, and typed reactivation binding while reopening the same expired operation at `claimed/preflight_compute`.
4. Keep Workspace suspended, `expired_unpaid`, and `autoRenew=false` until the full renewal succeeds.
5. Reuse the original debit, provider child, and Receipt identities. On success atomically restore paid-through/active/running and enable future auto-renew.
6. Prove response loss, concurrent keys, audit failure rollback, every persisted phase restart, exact one debit/provider/Receipt, and no new operation for the same period.
7. Stop before shared TableStore wiring if D still owns the reserved files. After the serialized join, run:

```bash
cd services/control-plane
go test -count=1 ./internal/server -run '^TestWorkspaceRenewalReactivation'
cd ../..
npm run verify:local
```

Run the exact PostgreSQL reactivation tests separately against the task-owned
isolated database, require zero skips, then commit/review/PR/merge/readback.

### Task 7a: serialize D and C-Renewal TableStore wiring

**Files:**
- Modify: `services/control-plane/internal/server/table_store.go`
- Modify: `services/control-plane/internal/server/memory_table_store_test.go`

**Steps:**
1. Freeze the Registration and Reactivation method signatures from their typed reducers and PostgreSQL implementations.
2. Add both narrow methods to the shared store interface in one commit and add equivalent memory behavior/tests without copying either reducer.
3. Rebase both capabilities on that commit one at a time and resolve no other file.
4. Run both focused suites, their exact PostgreSQL tests, and one `npm run verify:local` after the integration diff freezes. Do not run a full gate here.

### Task 8: N - freeze exact qualification and Release admission contracts offline

**Files:**
- Modify: `tools/cloud-candidate-receipt.ts`
- Create: `tools/cloud-release-admission.ts`
- Create: `tests/tools/cloud-release-admission.test.ts`
- Modify: `tools/local-workspace-qualification.ts`
- Modify: `tests/tools/local-workspace-qualification.test.ts`
- Modify: `packages/contracts/opl-cloud-candidate-receipt-contract.json`
- Modify: `packages/contracts/opl-cloud-distribution-contract.json`
- Create: `packages/contracts/opl-cloud-qualification-receipt-contract.json`
- Modify: `tools/validate-product-boundary.mjs`
- Modify: `tests/contracts/product-distribution.test.ts`
- Modify: `tests/contracts/clean-host-qualification.test.ts`
- Modify later in Task 12: the three owning workflows

**Steps:**
1. Define strict Candidate, Local, Instance, restore, alerts, public-edge, rollback, and publication receipt identities. Every receipt binds Product SHA/tree, index/children, Candidate receipt digest, schema version, owner, and provenance.
2. Reject extra/missing fields, reordered identity lists where order is canonical, malformed encodings, mismatched child digests, stale Candidate receipts, and duplicate/missing required receipt kinds.
3. Define an allowlist authority descriptor for every receipt kind: repository, workflow path/ref, protected environment when applicable, actor policy, artifact name, and attestation predicate. Admission receives only immutable locators, fetches the owner artifact/attestation, and rejects dispatcher-supplied JSON as evidence.
4. Validate every successful GitHub run and artifact: event/path, repository/ref, head branch/SHA, actor/triggering actor, run attempt, artifact ID/digest, expiry, and OIDC attestation where required. Candidate additionally validates bridge parent/diff shape.
5. Recompute the downloaded Candidate bundle, canonical manifest, portable asset checksums, GHCR index/children/revision, and receipt digest.
6. Keep this task read-only with fixture artifacts/attestations. It must not build, tag, publish, deploy, or call Instance.
7. Run the exact candidate, qualification, admission, distribution, and product-boundary tests followed by `npm run verify:local`; commit/review/PR only after the receipt enum is reconciled with J/K/L/M.

## Wave 1 Integration Gate

1. Wait until A, C Delete, D, E, M, C Renewal, the serialized TableStore join,
   and the offline N contract preparation accepted for this Wave are merged.
2. Fetch fresh canonical `main` and require a clean worktree. Record exact
   `HEAD` and `HEAD^{tree}` before verification.
3. Rerun only any alias-focused command whose merge resolution changed its
   owning files.
4. Run `npm run verify:local:full` once. Require every PostgreSQL package group
   to execute with zero skips and remove all temporary containers on success or
   failure.
5. Read back canonical remote `main`; if SHA/tree changed during the gate, the
   result is stale and must not be recorded as `wave_integrated`.
6. Record the exact SHA/tree and successful full-gate evidence, then begin Wave
   2. Do not construct a Candidate from this intermediate identity.

## Wave 2: Product Recovery, Restore, And Distribution

### Task 9: F/J - type changed operations and unify recovery projection

**Files:**
- Modify changed Registration/Delete/Renewal/Reset reducers only
- Create: `services/control-plane/internal/server/operator_recovery_projection.go`
- Create: `services/control-plane/internal/server/operator_recovery_projection_test.go`
- Modify: `services/control-plane/internal/server/routes_admin.go`
- Modify: Console operator DTO/controller/page files

**Steps:**
1. Audit only C/D/J touched decoders and transitions; reject unknown fields and illegal conversion while reading supported persisted versions explicitly.
2. Project each exceptional Registration/Delete/Reset operation once with stable ID, owner, blocker code, allowed action, audit status, and closure evidence.
3. Add bounded, typed, idempotent recovery commands that read owner state and write only the operation owner's state.
4. Complete Reset Apply from the existing preview with exact authority, CAS, owner readback, deterministic audit, and no mutation on residual conflict/unknown.
5. Prove restart reconstruction, bounded pages/actions, no duplicate audit/side effects, and clean closure.
6. Run the focused reducer/projection/recovery tests, exact PostgreSQL recovery tests with zero skips, and one `npm run verify:local`; commit/review/PR/merge.

### Task 10: L - emit stable active/recovered Cloud signals from persisted facts

**Files:**
- Modify: `services/control-plane/internal/server/operational_alerts.go`
- Modify: `services/control-plane/internal/server/operational_alerts_test.go`
- Modify: operator health/projection files only as required
- Modify: `packages/contracts/opl-cloud-qualification-receipt-contract.json`

**Steps:**
1. Derive signal identity and state from persisted J projection, database readiness, provider/Ledger owner readback, backup-readiness, and purchase-stop facts.
2. Emit deterministic redacted `active` and `recovered` facts; rebuild active state on restart and close only when the owning fact is terminally healthy.
3. Do not use the existing process-local dedupe map as the authority. Persist or deterministically project the transition cursor required to prove closure across restart using existing RuntimeOperation/audit persistence; do not add a migration after Schema Freeze.
4. Test repeated projection, restart, active-before-recovered ordering, no secret/customer identifier leakage, and unrelated-operation isolation.
5. Run the focused signal/projection/restart tests and one `npm run verify:local`; commit/review/PR/merge. External routing/ack/on-call remains an Instance receipt.

### Task 11: K - verify three isolated database restores through owner APIs

**Files:**
- Create: `tools/verify-database-restore.ts`
- Create: `tests/tools/verify-database-restore.test.ts`
- Modify: `tools/verify-local.ts`
- Modify: `tests/tools/verify-local.test.ts`
- Modify: `package.json`
- Modify: `docs/runtime/production-runbook.md`

**Steps:**
1. Enter only after D/M and every other database writer are merged and the three migration sets are frozen. If J/L required a migration, move this gate after that migration rather than reusing a stale restore fixture.
2. Use the pinned PostgreSQL image and create three separate source/restore databases for Control Plane, Fabric, and Ledger.
3. Start each owner, seed its public/API-owned identity and operation facts, and capture exact pre-dump owner readback.
4. `pg_dump` one owner database at a time, restore into a clean target, apply that owner's current migration chain, and start only that owner against the restored database.
5. Read back Account/Registration/Workspace/operation, Fabric binding/operation, and Ledger Receipt/reconciliation through the owning HTTP API. Compare canonical typed facts, not table dumps.
6. Prove cross-database isolation by making each other owner database unavailable during the current restore.
7. Reject wrong owner, stale schema journal, missing row, duplicate Receipt, and identity drift.
8. Expose a standalone focused restore-qualification command rather than hiding restore evidence inside every module's full suite. Clean all containers/volumes on success and failure.
9. Run focused tool tests, the standalone three-owner restore qualification, and `npm run verify:local`; commit/review/PR/merge.

### Task 12: N/G - remove Release rebuild and consume the exact Candidate

**Files:**
- Modify: `.github/workflows/build-opl-cloud-candidate.yml`
- Modify: `.github/workflows/clean-host-qualification.yml`
- Modify: `.github/workflows/release-opl-cloud-image.yml`
- Modify Task 8 validators/contracts/tests
- Modify: `tools/local-workspace-qualification.ts`

**Steps:**
1. Add canonical Candidate inputs `product_sha` and `product_tree`; require fresh remote `main HEAD == product_sha` and checked-out `HEAD^{tree} == product_tree` before archive/build. This lands on canonical `main` before any bridge is created.
2. Make Local qualification consume a Candidate manifest and bind exact Product SHA/tree, index/children, Candidate receipt digest, portable assets, and business receipt.
3. Change the Local business journey to public Registration, zero-balance rejection, administrator top-up, exact-balance purchase, restart/readback, Delete, and zero residue.
4. Change Release workflow inputs to immutable artifact/attestation locators for Candidate and every required owner receipt. Fetch and verify their allowlisted workflow authority before parsing receipt content.
5. Download and revalidate the original Candidate artifact and GHCR identity.
6. Remove every image build from Release. Promote only with:

```bash
docker buildx imagetools create \
  --tag "$IMAGE_REPOSITORY:$RELEASE_TAG" \
  "$IMAGE_REPOSITORY@$CANDIDATE_DIGEST"
```

7. Read back the tag digest and require exact equality with the Candidate digest. Reuse original portable asset bytes; generated release metadata must refer to their unchanged checksums.
8. Assert workflow permissions, publisher identity, protected environment, authority-rooted receipt admission, no `buildx build`, and publication/readback/attestation behavior in contract tests.
9. Run the exact workflow, admission, qualification, and distribution contract tests followed by `npm run verify:local`; commit/review/PR/merge/readback.

## Wave 2 Integration Gate

1. Wait until F/J, L, K, and N/G Cloud implementation are merged and no schema,
   contract, workflow, or portable-asset writer remains active.
2. Fetch fresh canonical `main`, require a clean worktree, and record exact
   `HEAD` and `HEAD^{tree}`.
3. Run every A-N focused command whose final merge resolution changed owning
   files, then run the standalone K restore qualification.
4. Run `npm run verify:local:full` once with zero PostgreSQL skips and complete
   cleanup on success or failure.
5. Read back remote `main`. Record this SHA/tree and gate evidence only when the
   remote identity remains exact. This result may serve as the Pre-Candidate
   full gate if no tracked Product byte changes afterward.

## Wave 3: Canonical Join And Exact Candidate

### Task 13: freeze final identity and admit the Pre-Candidate gate

1. Fetch fresh remote `main`. Shared `server.go`, `routes_admin.go`,
   `table_store.go`, migration registry, and machine-contract joins must already
   be integrated; do not invent last-writer resolution here.
2. Require clean status and `git diff --check`, then compare exact `HEAD` and
   `HEAD^{tree}` with the successful Wave 2 Integration Gate.
3. If both identities are unchanged, revalidate the retained Wave 2 evidence
   and reuse it as the Pre-Candidate full gate. Do not rerun a byte-identical
   full suite.
4. If any source, schema, contract, workflow, portable asset, or generated byte
   changed, rerun the affected focused commands, `npm run verify:local`, and one
   `npm run verify:local:full` with zero skips. Bind the replacement evidence to
   the new exact SHA/tree.
5. Read back remote canonical `main`, then freeze the admitted SHA/tree. No
   later Product byte may reuse its Candidate or qualification receipts.

### Task 14: construct one strict Candidate bridge and qualify locally

1. Create `codex/candidate-<short-sha>-bridge` as one child of the frozen Product SHA.
2. Confirm the canonical Product SHA already contains the reusable `main HEAD == PRODUCT_SHA` and checked-out tree equality checks from Task 12.
3. Change only the Candidate workflow dispatch expression. Bind literal repository, full ref, `RenDeHuang` actor and triggering actor, full Product SHA, and exact Product tree inputs; never add identity logic only on the bridge.
4. Push the bridge, read back commit parent/tree/diff, dispatch exactly that ref/SHA, and wait for a successful run.
5. Read back run actor/provenance, unique artifact ID/digest, Candidate manifest/receipt digest, bundle checksums, GHCR index/children/revisions, and bridge commit shape.
6. Select exactly one attempt. Preserve its immutable receipt separately from the bridge source.
7. Run Local qualification for that exact Candidate and return the Local receipt bound to the same Candidate identity.
8. Keep the bridge until formal Release admission has independently revalidated it; never merge it into `main`.

### Task 15: Instance handoff and formal exact-byte Release

1. Send the exact Candidate manifest/digest, strict receipt contract, and allowlisted protected Instance workflow authority descriptor to the Instance owner. Cloud does not dispatch deployment or access the private production network.
2. Fetch Tencent/TKE, backup/restore, alert/on-call, public-edge, and executed rollback artifacts/attestations from the allowed owner workflows. Admit them only when run provenance, artifact digest, attestation, and all exact Candidate identities match.
3. Missing or conflicting receipts remain `external_owner_pending`; do not infer production readiness from Cloud tests.
4. Once all receipts and publication authority are present, dispatch Release from canonical `main` and promote the admitted Candidate digest without rebuild.
5. Read back GHCR Release tag digest, GitHub Release target/assets/checksums/attestations, and publication Receipt. Require exact equality with the Candidate digest and original portable bytes.
6. Delete the task-owned Candidate bridge and temporary refs only after Release readback is complete. Record cleanup and final remote `main`/Release identity.
