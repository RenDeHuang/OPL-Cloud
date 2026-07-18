# Slides 4 and 8-10 Production Proof Implementation Plan

> **Historical / Superseded - do not execute.** Current Pilot authority is the
> dual Acceptance and one-request Basic release contract; no production action is authorized here.

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task. Repository policy forbids subagents for this work.

**Goal:** Deploy the integrated Pilot B candidate safely, create/reuse the one fixed Provider Acceptance slot, prove the real Workspace/Gateway path, and retain migration/recovery/reconciliation evidence.

**Architecture:** Create this branch only from the fully merged integration HEAD. Resolve the first-slot deadlock with one explicitly confirmed bootstrap job inside the deploy workflow: deploy candidate, run the existing one-time Provider Acceptance job, then run normal live QA in the same rollback group. Reuse the PostgreSQL migration runner, immutable image workflows, fixed slot, and grouped rollback; add no paid per-run verifier.

**Tech Stack:** GitHub Actions, Kubernetes/TKE, TencentDB for PostgreSQL, existing Node/Playwright verification tools, Go/Node tests.

---

## Creation Gate

Do not create `ops/slides-4-8-10-production` until Slides 1-3, Slides 5/7,
Slide 6, and the Vue UI-only checkpoint are merged into
`integration/pilot-launch-b`. Create it from that exact integration HEAD.

## File Map

- Modify `.github/workflows/deploy-tke-production.yml`: guarded first-slot bootstrap and rollback conditions.
- Modify `.github/workflows/provider-acceptance.yml`: reusable entry while preserving manual dispatch.
- Modify `tools/provider-acceptance.ts` only if integration tests expose a machine-token/bootstrap defect.
- Modify `tools/production-live-qa.ts`: verify billing list, usage/stats, owner credential flow, and stable provider IDs.
- Modify `tools/production-verifier.ts`: align read-only fixed-slot checks with candidate APIs.
- Modify `tests/production/provider-acceptance.test.ts` and production workflow contract tests.
- Modify `packages/contracts/opl-cloud-deployment-contract.json`, `packages/contracts/opl-cloud-launch-freeze-contract.json`, and `docs/runtime/production-runbook.md` only with matching proof.
- Create `docs/runtime/pilot-b-production-evidence.md`: redacted runs, commits, digests, counts, journal, and gate results.

Do not change pricing, billing protocol, customer APIs, Runtime authorization,
Sub2API, Fabric procurement semantics, auto-renew policy, replicas, or Deployment
strategy in this lane.

### Task 1: Create the production worktree from integrated code

- [ ] **Step 1: Verify integration is complete and clean**

Run from the root integration worktree:

```bash
git status --short --branch
git log -1 --format='%H %s'
git branch --contains HEAD
```

Expected: clean `integration/pilot-launch-b`; all four implementation merge
commits are present.

- [ ] **Step 2: Create the branch/worktree**

Run:

```bash
git worktree add .worktrees/slides-4-8-10-production -b ops/slides-4-8-10-production integration/pilot-launch-b
```

Expected: isolated worktree on the integrated HEAD.

- [ ] **Step 3: Install and run the baseline**

Run in the new worktree:

```bash
npm ci
(cd services/internal/postgresmigrate && go mod download && go test ./...)
(cd services/ledger && go mod download && go test ./...)
(cd services/fabric && go mod download && go test ./...)
(cd services/control-plane && go mod download && go test ./...)
npm test
```

Expected: PASS before production workflow edits.

### Task 2: Remove the first-slot deployment deadlock

- [ ] **Step 1: Write failing workflow contract tests**

Add assertions for exactly two modes:

```text
normal: deploy -> live-qa -> retire legacy secret
bootstrap: deploy -> provider-acceptance -> live-qa -> retire legacy secret
```

Both modes enter grouped rollback if deploy or required verification fails.
Bootstrap requires both the existing Provider Acceptance confirmation and a
separate deploy-bootstrap confirmation. Ordinary runs never call Provider
Acceptance.

- [ ] **Step 2: Add reusable Provider Acceptance entry**

Keep `workflow_dispatch` and add `workflow_call` inputs/secrets for the fixed
account and exact confirmation. Both execute the same job and tool. Fixed facts:

```text
account: acct-verification-slot-01
slot: verification-slot-01
idempotency key: provider-acceptance:verification-slot-01
confirmation: I_UNDERSTAND_THIS_BUYS_ONE_PREPAID_CVM_AND_CBS
```

- [ ] **Step 3: Add guarded deploy bootstrap inputs**

Add boolean `bootstrap_verification_slot=false` and string
`bootstrap_confirmation`. Bootstrap validation requires:

```text
I_UNDERSTAND_BOOTSTRAP_DEPLOYS_BEFORE_THE_FIRST_FIXED_SLOT_QA
```

The reusable Provider Acceptance job runs only in bootstrap mode. Live QA uses
`always()` and runs after deploy plus either successful bootstrap acceptance or
a skipped bootstrap job. Legacy Secret cleanup runs only after live QA success.

- [ ] **Step 4: Make rollback cover both modes**

Rollback triggers on deploy failure, bootstrap Provider Acceptance failure, or
live QA failure. It restores the complete previous Cloud/Workspace image set.
It never rolls back or deletes Tencent CVM/CBS/PV resources.

- [ ] **Step 5: Run tests and commit**

Run:

```bash
npm test
git diff --check
git add .github/workflows/deploy-tke-production.yml .github/workflows/provider-acceptance.yml tests/production/provider-acceptance.test.ts packages/contracts/opl-cloud-deployment-contract.json
git commit -m "fix(release): bootstrap fixed slot before first live qa"
```

Expected: PASS; normal deployments cannot buy provider resources.

### Task 3: Rehearse migrations and recovery before deployment

- [ ] **Step 1: Run duplicate-data preflight read-only**

With `DATABASE_URL_READONLY` exported outside shell history, run:

```bash
psql "$DATABASE_URL_READONLY" -v ON_ERROR_STOP=1 -c "SELECT sub2api_user_id, count(*) FROM control_plane_accounts WHERE sub2api_user_id > 0 GROUP BY sub2api_user_id HAVING count(*) > 1"
psql "$DATABASE_URL_READONLY" -v ON_ERROR_STOP=1 -c "SELECT COALESCE(NULLIF(account_id, ''), owner_account_id) AS account_id, count(*) FROM control_plane_workspaces WHERE COALESCE(NULLIF(account_id, ''), owner_account_id) <> '' GROUP BY 1 HAVING count(*) > 1"
```

Expected: zero rows. Any row blocks deployment; do not add an automatic
deduplicator.

- [ ] **Step 2: Record pre-migration journal and counts**

Run the journal and count SQL in `docs/runtime/production-runbook.md`. Record
counts only, never row data, in the operations evidence system and redacted
evidence document.

- [ ] **Step 3: Verify automatic backup**

In TencentDB, confirm the configured automatic backup schedule and one completed
automatic backup. Record backup ID, completion time, retention, and external
screenshot reference. A manual backup alone does not pass.

- [ ] **Step 4: Restore to an isolated instance**

This is billable and requires explicit operator approval immediately before the
TencentDB action. Restore in the same private VPC with no public endpoint,
restricted security group, fixed storage, and no production cutover.

- [ ] **Step 5: Run migrations on the isolated restore**

Start one candidate Control Plane, Fabric, and Ledger against the restore, then
query:

```sql
SELECT service, version, applied_at
FROM opl_schema_migrations
ORDER BY service, version;
```

Expected: each new version once. Restart all three; rows and timestamps remain
unchanged.

- [ ] **Step 6: Compare counts and PostgreSQL metrics**

Compare account, compute, storage, Fabric operation, ownership, receipt, and
journal counts. Capture WAL, CPU, storage, and restart graphs. Repeated DDL,
backfill, or unexplained count change blocks production.

### Task 4: Build immutable candidate images from one revision

- [ ] **Step 1: Freeze and test the candidate**

Run:

```bash
git rev-parse HEAD
git status --short
(cd services/internal/postgresmigrate && go test ./...)
(cd services/ledger && go test ./...)
(cd services/fabric && go test ./...)
(cd services/control-plane && go test ./...)
npm test
npm run build
```

Expected: clean branch and PASS. Record the SHA.

- [ ] **Step 2: Dispatch image release**

Build Cloud from the exact SHA and mirror the frozen Workspace source digest.
Record workflow run ID and both `repository@sha256` outputs. Tags and `latest`
fail the gate.

- [ ] **Step 3: Verify provenance and remote security state**

Cloud OCI revision must equal the candidate SHA; Workspace config/revision must
equal contract. If remote `main` contains an unincluded security fix, rebase
integration and rebuild before deployment.

### Task 5: Bootstrap the fixed slot and run live QA

- [ ] **Step 1: Dispatch bootstrap deploy exactly once**

Use immutable digests, set `bootstrap_verification_slot=true`, and supply both
confirmations. The run must deploy, execute one Provider Acceptance operation,
then run live QA. Record run IDs and rollback artifact ID.

- [ ] **Step 2: Validate Provider Acceptance facts**

The manifest/operator state must prove:

```text
verification-slot-01
SA5.MEDIUM4 / 2c4g
10GB CBS
PREPAID, one month
NOTIFY_AND_MANUAL_RENEW
one stable CVM ID, CBS ID, provider operation ID
no delete, destroy, or renew
```

Ambiguous inventory or a second candidate stops without purchase.

- [ ] **Step 3: Validate live QA facts**

Prove login, ready Runtime, HTTP 101 WebSocket exchange, one real model response,
request usage row, aggregate stats, exact `actualCostUsdMicros`, billing receipt
list, and unchanged fixed CVM/CBS/PV IDs. Artifacts exclude cookies, passwords,
Keys, prompts, responses, and raw admin payloads.

- [ ] **Step 4: Prove Provider Acceptance replay**

Dispatch standalone Acceptance with the same identity. Expected: `reused`, zero
purchase, identical CVM/CBS IDs.

- [ ] **Step 5: Run one normal deployment**

Dispatch the same digests with bootstrap disabled. Expected: deploy, live QA,
legacy global Secret retirement. This proves future deployments do not need the
bootstrap exception.

### Task 6: Verify production state and reconciliation

- [ ] **Step 1: Verify rollout and exact images**

Run:

```bash
kubectl -n opl-cloud rollout status deployment/opl-cloud-control-plane --timeout=5m
kubectl -n opl-cloud rollout status deployment/opl-cloud-fabric --timeout=5m
kubectl -n opl-cloud rollout status deployment/opl-cloud-ledger --timeout=5m
kubectl -n opl-cloud get deployment opl-cloud-control-plane opl-cloud-fabric opl-cloud-ledger -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.template.spec.containers[0].image}{"\n"}{end}'
```

Expected: available and exact candidate digest.

- [ ] **Step 2: Verify public/internal readiness**

Run the health/readiness and temporary port-forward checks from the runbook.
Stop each port-forward afterward. Readiness alone is not acceptance.

- [ ] **Step 3: Run server-computed reconciliation**

Use an operator session and fresh Idempotency-Key. Expected: `status=ok`, report
appended in Ledger, purchase guard open. Mismatch blocks opening; do not auto-fix.

- [ ] **Step 4: Restart once and prove no migration replay**

Capture journal, restart all three through the supported rollout path, then
compare journal timestamps and WAL/CPU/storage. Expected: no new migration row
or migration-related spike.

### Task 7: Record evidence and close stale contracts

- [ ] **Step 1: Write redacted evidence**

Record exact SHA, workflow run IDs, immutable digests, migration versions, count
comparisons, hashed provider IDs, reconciliation report ID, and all gate results.
Link external screenshots/metrics by opaque evidence ID; commit no secrets or raw
responses.

- [ ] **Step 2: Update contracts and runbook**

Mark only proven facts. Keep automatic renewal, Pro/GPU, multi-replica HA, and
SSO outside Pilot. Replace stale blocked-verifier text with the fixed-slot chain
actually run.

- [ ] **Step 3: Final verification and commit**

Run:

```bash
(cd services/internal/postgresmigrate && go test ./...)
(cd services/ledger && go test ./...)
(cd services/fabric && go test ./...)
(cd services/control-plane && go test ./...)
npm test
npm run build
git diff --check integration/pilot-launch-b...HEAD
git add .github/workflows/deploy-tke-production.yml .github/workflows/provider-acceptance.yml tools/production-live-qa.ts tools/production-verifier.ts tools/provider-acceptance.ts tests/production packages/contracts/opl-cloud-deployment-contract.json packages/contracts/opl-cloud-launch-freeze-contract.json docs/runtime/production-runbook.md docs/runtime/pilot-b-production-evidence.md
git commit -m "docs: record pilot b production acceptance"
```

Expected: PASS and a production-proof-only diff.

- [ ] **Step 4: Rebase and hand off**

Run:

```bash
git rebase integration/pilot-launch-b
(cd services/internal/postgresmigrate && go test ./...)
(cd services/ledger && go test ./...)
(cd services/fabric && go test ./...)
(cd services/control-plane && go test ./...)
npm test
```

Expected: PASS. Merge only from the root integration worktree, then rerun the
full suite before the final PR to `main`.
