# OPL Cloud Launch Delivery V2 Implementation Plan

> **Superseded:** Do not execute this historical plan. Use the four slide-bounded
> plans linked by
> `docs/superpowers/specs/2026-07-16-pilot-b-rolling-four-lanes-design.md`.
> The active Pilot excludes Pro, React implementation, Key CRUD, and subagents.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Ship Basic and Pro monthly Workspaces on one-month Tencent PREPAID CVM/CBS, account-owned Sub2API keys, a recoverable Console flow, and a reusable production verification slot.

**Architecture:** Control Plane remains the product orchestrator, Fabric remains the only Tencent/Kubernetes writer, deployed Sub2API v0.1.156 remains unchanged, and Ledger remains evidence-only. Cloud also accepts the API-compatible v0.1.155 rollback. Three file-disjoint worker worktrees merge into one integration branch, which is shipped through one PR and one guarded rollout.

**Tech Stack:** Go 1.22, Node.js test runner, React 19, TypeScript, Ant Design, Tencent Cloud Go SDK, Kubernetes/Tencent CBS CSI, GitHub Actions, PostgreSQL.

---

## Global Constraints

- Never modify, restart, redeploy, or migrate Sub2API.
- Never run the current paid production verifier.
- Never create Tencent resources until fake/provider-shape/CI checks pass and read-only inventory proves no reusable slot exists.
- One allowed slot purchase means PREPAID for one month, manual renewal, the cheapest TKE-compatible CVM meeting App minimums, and minimum supported PREPAID CBS. Record its IDs and prohibit later purchases.
- Worker branches own disjoint files. Only the integration worktree updates shared contracts, merges, pushes, opens a PR, or deploys.
- Every behavior change follows RED -> GREEN with focused commands captured in the task handoff.

## Phase 1: Contract And Cleanup

### Task 1: Replace The Launch Freeze With V2 Product Truth

**Files:**
- Modify: tests/contracts/launch-architecture-freeze.test.ts
- Modify: packages/contracts/opl-cloud-launch-freeze-contract.json
- Modify: packages/contracts/opl-cloud-product-contract.json
- Modify: packages/contracts/opl-cloud-pricing-contract.json
- Modify: packages/contracts/opl-cloud-deployment-contract.json
- Modify: docs/invariants.md
- Modify: docs/architecture.md
- Modify: docs/status.md
- Modify: docs/runtime/production-runbook.md

- [ ] **Step 1: Write the failing V2 contract test**

Add exact assertions:

    assert.deepEqual(Object.keys(freeze.productSurfaces), ["gateway", "workspace", "console", "fabric", "ledger"]);
    assert.deepEqual(Object.keys(freeze.ownerLanes), ["console", "fabric", "gateway", "ledger"]);
    assert.deepEqual(freeze.customerProducts.basic, {
      compute: { cpu: 2, memoryGb: 4, cnyCents: 35000, usdMicros: 50000000 },
      storage: { sizeGb: 10, cnyCents: 1800, usdMicros: 2571429 },
      targetSaleable: true
    });
    assert.deepEqual(freeze.customerProducts.pro, {
      compute: { cpu: 8, memoryGb: 16, cnyCents: 150000, usdMicros: 214285715 },
      storage: { sizeGb: 100, cnyCents: 18000, usdMicros: 25714286 },
      targetSaleable: true
    });
    assert.deepEqual(freeze.monthlySettlement.protocol, ["debit", "provision", "claim", "activate"]);
    assert.equal(freeze.monthlySettlement.confirmedNoResourceAfterDebit, "idempotent_refund");
    assert.equal(freeze.monthlySettlement.partialOrUnknownProviderResult, "manual_review_without_refund");
    assert.equal(freeze.providerProcurement.chargeType, "PREPAID");
    assert.equal(freeze.workspaceRuntime.sourceImage.digest, "sha256:9d867fe0fc9db48b6efa27371d77770e46fc8cd97d26ef85a81fbdac7e96ca76");
    assert.equal(freeze.gateway.sub2apiMutable, false);
    assert.equal(freeze.gateway.keyName, "opl-workspace");
    assert.equal(freeze.gateway.adminUsageEndpointAllowed, false);
    assert.equal(freeze.verification.purchaseBudget, 1);
    assert.equal(freeze.verification.perRunTencentPurchase, false);
    assert.equal(freeze.launchStages.length, 10);
    assert.equal("slides" in freeze, false);
    assert.equal(freeze.deliveryPhases.length, 6);

- [ ] **Step 2: Run RED**

    node --test tests/contracts/launch-architecture-freeze.test.ts

Expected: FAIL on missing V2 fields and old slides.

- [ ] **Step 3: Implement the machine and human contracts**

Use PREPAID/period 1/manual renewal, debit -> provision -> claim -> activate, deterministic refund only after confirmed absence, manual review for ambiguous provider state, five product surfaces, four owner lanes, ten launch stages, six delivery phases, named opl-workspace Key, v0.1.155/v0.1.156 Key DTO usage, and the pinned App source digest. Delete provision-before-charge and paid-per-run narratives.

- [ ] **Step 4: Run GREEN**

    node --test tests/contracts/launch-architecture-freeze.test.ts tests/contracts/product-contract.test.ts tests/contracts/monthly-billing-hard-cut.test.ts
    git diff --check

Expected: all tests pass; diff check is silent.

- [ ] **Step 5: Commit**

    git add AGENTS.md .gitignore docs packages/contracts tests/contracts
    git commit -m "docs: lock launch delivery v2 contracts"

### Task 2: Retire Stale Git State And Create Worker Worktrees

**Files:**
- Main worktree only: .gitignore
- Main worktree only: untracked AGENTS.md
- No product source changes

- [ ] **Step 1: Prove main changes are subsumed**

    git -C /home/dev/medopl-3 diff -- .gitignore
    diff -u /home/dev/medopl-3/AGENTS.md /home/dev/medopl-3/.worktrees/launch-architecture-freeze/AGENTS.md || true
    git -C /home/dev/medopl-3 check-ignore -v .worktrees .codegraph

Expected: only CodeGraph differs; integration AGENTS includes that block plus launch rules; both generated directories are ignored.

- [ ] **Step 2: Clean only duplicated main files**

Use apply_patch to remove the uncommitted /.codegraph/ line from main .gitignore and delete main's untracked AGENTS.md. Verify main has no file entries. Do not use broad restore/reset.

- [ ] **Step 3: Rename the integration branch**

    git branch -m feat/launch-delivery-v2

- [ ] **Step 4: Remove obsolete worktrees, branches, and stashes**

After clean-state checks, remove workspace-runtime-p0 and launch-p0-business-chain worktrees. Delete only the audited local branches: fix/workspace-runtime-p0, fix/launch-p0-business-chain, fix/console-e2e-nested-route, fix/tke-native-node-pool-network, feature/unified-gateway-contract, feature/opl-gateway, plan/monthly-unified-ledger, feat/production-resilience-e2e. Drop all three audited stashes. Do not delete remote branches yet.

- [ ] **Step 5: Create three workers from the integration HEAD**

    git worktree add .worktrees/fabric-prepaid-workspace -b fix/fabric-prepaid-workspace feat/launch-delivery-v2
    git worktree add .worktrees/gateway-commercial -b feat/gateway-commercial feat/launch-delivery-v2
    git worktree add .worktrees/launch-ux-release -b feat/launch-ux-release feat/launch-delivery-v2

- [ ] **Step 6: Verify clean baselines**

    npm test
    npm run typecheck
    (cd services/fabric && go test ./...)
    (cd services/control-plane && go test ./...)

Expected: all pass before worker edits.

## Phase 2: Fabric PREPAID And Workspace

### Task 3: Convert Native CVM Capacity And Creation To PREPAID

**Files:**
- Modify: services/fabric/cmd/opl-tencent-provisioner/main.go
- Modify: services/fabric/cmd/opl-tencent-provisioner/main_test.go

- [ ] **Step 1: Write PREPAID request/readback tests**

Assert InstanceChargeType PREPAID and:

    InstanceChargePrepaid: &tke2022.InstanceChargePrepaid{
      Period: common.Uint64Ptr(1),
      RenewFlag: common.StringPtr("NOTIFY_AND_MANUAL_RENEW"),
    }

Change prepaid-pool cases from expected rejection to acceptance. Require PREPAID SKU filtering, no PostPaidQuotaSet request, and rejection of POSTPAID/missing period/automatic renewal readback.

- [ ] **Step 2: Run RED**

    cd services/fabric
    go test ./cmd/opl-tencent-provisioner -run 'Test(BuildCreateNativeNodePoolRequest|TencentSDKCapacity|TencentSDKClientMutation)' -count=1

- [ ] **Step 3: Implement minimum PREPAID shape**

Set exact request/readback fields. Keep subnet, SKU SELL, capacity, label, replay, and no-mutation preflight checks. Remove only the invalid PostPaidQuotaSet dependency; add no new dependency.

- [ ] **Step 4: Run GREEN and commit**

    go test ./cmd/opl-tencent-provisioner -count=1
    go test -race ./cmd/opl-tencent-provisioner -count=1
    git add cmd/opl-tencent-provisioner
    git commit -m "fix(fabric): require prepaid native cvm"

### Task 4: Create PREPAID CBS And Account-Scoped Workspace Secrets

**Files:**
- Modify: services/fabric/cmd/opl-tencent-provisioner/main.go
- Modify: services/fabric/cmd/opl-tencent-provisioner/main_test.go
- Modify: services/fabric/internal/fabric/types.go
- Modify: services/fabric/internal/fabric/service.go
- Modify: services/fabric/internal/fabric/service_test.go
- Modify: services/fabric/internal/fabric/tencent_provider.go
- Modify: services/fabric/internal/fabric/tencent_provider_test.go
- Modify: services/fabric/internal/http/server.go
- Modify: services/fabric/internal/http/server_test.go

- [ ] **Step 1: Write CBS and Secret RED tests**

Require CreateDisks with Placement.Zone, PREPAID, one disk, stable ClientToken, exact size/tags, and:

    DiskChargePrepaid: &cbs2017.DiskChargePrepaid{
      Period: common.Uint64Ptr(1),
      RenewFlag: common.StringPtr("NOTIFY_AND_MANUAL_RENEW"),
    }

Require DescribeDisks readback of ID/state/type/renew flag/size/zone/deadline. Require PV driver com.tencent.cloud.csi.cbs, volumeHandle disk-*, Retain, RWO, empty storage class, Zone affinity, and PVC volumeName. Require a dedicated Secret-write response containing only secretRef/version/fingerprint and no raw Key.
Require the existing Fabric catalog and create path to accept the approved Pro 8c16g plan alongside Basic.

- [ ] **Step 2: Run RED**

    cd services/fabric
    go test ./cmd/opl-tencent-provisioner ./internal/fabric ./internal/http -run 'Test.*(Prepaid|Storage|Secret|Workspace)' -count=1

- [ ] **Step 3: Extend the existing provisioner protocol**

Add create/sync/renew storage actions to the existing request/response/client switch. Reuse installed CBS SDK and stable IDs. Require one configured launch Zone and fail before purchase on NodePool/storage Zone mismatch. Treat UNATTACHED or ATTACHED as provider-ready and PVC Bound as Kubernetes-ready.

- [ ] **Step 4: Replace dynamic PVC and global Key**

Create PV then PVC and return the real disk ID. Preserve CBS on PVC/PV removal and report retained/released without provider termination evidence. Add GatewaySecretRef to WorkspaceRuntimeInput. A dedicated transient Secret request writes/rotates the account K8s Secret; runtime receives only the ref. Remove production use of global OPL_CODEX_API_KEY.
Enable Pro only in the existing Fabric catalog and package-plan switch; do not add a second catalog.

- [ ] **Step 5: Run GREEN and commit**

    go test ./cmd/opl-tencent-provisioner ./internal/fabric ./internal/http -count=1
    go test -race ./... -count=1
    git add .
    git commit -m "feat(fabric): provision prepaid cbs workspaces"

## Phase 3: Gateway Account Projection

### Task 5: Read Existing Account Keys And Usage Without Copying Them

**Files:**
- Modify: services/control-plane/internal/clients/sub2api.go
- Modify: services/control-plane/internal/clients/sub2api_test.go
- Modify: services/control-plane/internal/clients/fabric.go
- Modify: services/control-plane/internal/clients/fabric_test.go
- Modify: services/control-plane/internal/controlplane/service.go
- Modify: services/control-plane/internal/server/server.go
- Create: services/control-plane/internal/server/routes_gateway.go
- Modify: services/control-plane/internal/server/console_tenant_isolation_test.go
- Modify: services/control-plane/internal/server/server_test.go

- [ ] **Step 1: Write client and tenant RED tests**

Desired Sub2APIKey fields are ID, UserID, Name, Key, Status, quota/quota-used, usage 5h/1d/7d, and last-used. Test paginated GET /api/v1/admin/users/:id/api-keys, strict user ID, exactly one active opl-workspace selection, zero/multiple failure, response-size limit, and redacted errors. Test GET /api/gateway/summary is current-account-only and returns Cache-Control private, no-store.

- [ ] **Step 2: Run RED**

    cd services/control-plane
    go test ./internal/clients ./internal/server -run 'Test.*(Sub2APIKey|Gateway|Tenant)' -count=1

- [ ] **Step 3: Implement one product-specific projection**

Extend the existing authenticated Sub2API client; do not create a Gateway service. Return live balance/account status, full owner-only Key, Key status, quota, Key DTO 5h/1d/7d usage, and last-used. Never call mock admin usage. Never accept caller-supplied Sub2API user IDs.

- [ ] **Step 4: Add JIT Fabric Secret handoff**

On Workspace create/rotate, resolve the mapped account's single active opl-workspace Key, call Fabric Secret writer, then pass only the ref into runtime creation. Missing/ambiguous Key fails before Workspace deployment.

- [ ] **Step 5: Run GREEN and commit**

    go test ./internal/clients ./internal/controlplane ./internal/server -count=1
    go test -race ./internal/clients ./internal/server -count=1
    git add .
    git commit -m "feat(gateway): project account key and usage"

## Phase 4: Commercial Plans And Settlement

### Task 6: Sell Basic And Pro With Debit-First Recovery

**Files:**
- Modify: services/control-plane/internal/server/pricing.go
- Modify: services/control-plane/internal/server/pricing_monthly_test.go
- Modify: services/control-plane/internal/server/monthly_billing.go
- Modify: services/control-plane/internal/server/monthly_billing_test.go
- Modify: services/control-plane/internal/server/routes_resources.go
- Modify: services/control-plane/internal/server/renewal_worker.go
- Modify: services/control-plane/internal/server/renewal_worker_test.go
- Modify: services/control-plane/internal/server/table_store.go
- Modify: services/control-plane/internal/server/ent_state_store.go

- [ ] **Step 1: Write exact-price and order RED tests**

Require Basic compute 35000 cents/50000000 micros, Pro compute 150000/214285715, 10GB storage 1800/2571429, 100GB storage 18000/25714286. Require event order balance -> debit -> fabric.create -> fabric.sync -> ledger. Confirmed absence invokes one deterministic positive refund; partial/unknown state enters manual review without refund; replay keeps one debit/refund/provider purchase; Pro is accepted by Control Plane pricing. Task 4 owns Fabric catalog acceptance.

- [ ] **Step 2: Run RED**

    cd services/control-plane
    go test ./internal/server -run 'Test.*(Pricing|MonthlyPurchase|Refund|Pro|Renew)' -count=1

- [ ] **Step 3: Implement exact products and debit-first state machine**

Reuse integer conversion. Keep compute/storage separate entitlement records. Keep deterministic Redeem Code charging and derive one deterministic positive refund code. Only refund after provider truth proves absence. Do not retry unknown provider writes with a new identity. Keep automatic renewal false; a renewal advances paidThrough only after one debit, one provider renewal, and readback.

- [ ] **Step 4: Run GREEN and commit**

    go test ./internal/server -count=1
    go test -race ./internal/server -count=1
    (cd ../internal/postgresmigrate && go test -race ./... -count=1)
    git add internal/server
    git commit -m "feat(console): sell monthly basic and pro plans"

## Phase 5: Console UX And Release Safety

### Task 7: Make Launch Recoverable And Show The Real Gateway

**Files:**
- Modify: apps/console-ui/src/api/auth-api.ts
- Modify: apps/console-ui/src/api/console-api.ts
- Modify: apps/console-ui/src/api/console-read-api.ts
- Modify: apps/console-ui/src/main.tsx
- Modify: apps/console-ui/src/pages/ConsolePage.tsx
- Modify: apps/console-ui/src/pages/billing/BillingPage.tsx
- Modify: apps/console-ui/src/pages/gateway/GatewayPage.tsx
- Modify: apps/console-ui/src/pages/workspaces/CreateWorkspacePage.tsx
- Modify: apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx
- Modify: apps/console-ui/src/styles.css
- Modify: tests/ui/commercial-console-surface.test.ts
- Modify: tests/ui/monthly-billing-story.test.ts
- Create: tests/ui/console-recovery-flow.test.ts

- [ ] **Step 1: Write UI RED tests**

Require AbortSignal.timeout, only 401 as unauthenticated, retryable auth errors, distinct auth/lazy/state loading, preserved redirect, Workspace 10-second polling capped at 30 attempts, ready/terminal stop, actual errors and manual retry. Require server-sourced Basic 350+18 and Pro 1500+180. Require Gateway fetch only on page, masked Key by default, explicit reveal/copy, no browser storage, and no architecture explanation.

- [ ] **Step 2: Run RED**

    node --test tests/ui/console-recovery-flow.test.ts tests/ui/commercial-console-surface.test.ts tests/ui/monthly-billing-story.test.ts

- [ ] **Step 3: Implement native recovery and guided flow**

Use AbortSignal.timeout(10000), no new dependency. Keep Suspense fallback only for lazy loading. Reuse current resource commands for one six-step plan/storage/debit/PREPAID/Secret-runtime/URL guide; do not add a second purchase path. Clear Gateway summary on route leave/logout and do not render raw Key until reveal.

- [ ] **Step 4: Run GREEN and commit**

    node --test tests/ui/console-recovery-flow.test.ts tests/ui/commercial-console-surface.test.ts tests/ui/monthly-billing-story.test.ts
    npm run typecheck
    npm run lint
    npm run build
    git add apps/console-ui tests/ui
    git commit -m "feat(console): guide and recover workspace launch"

### Task 8: Disable Paid Verification And Pin The App Digest

**Files:**
- Modify: .github/workflows/release-opl-cloud-image.yml
- Modify: .github/workflows/verify-production-chain.yml
- Modify: .github/workflows/deploy-tke-production.yml
- Modify: tools/production-verifier.ts
- Modify: tests/production/production-verifier.test.ts
- Modify: tests/production/tke-deploy-workflow.test.ts
- Modify: tests/production/production-readiness.test.ts
- Modify: tests/production/production-manifest.test.ts

- [ ] **Step 1: Write release RED tests**

Require source/target digests, reject latest and tag-only production refs, capture previous Cloud/Workspace images, and prohibit ordinary verifier create/destroy/paid-confirmation paths. Require fixed verification-slot-01 and refusal to purchase when one exists, multiple are found, state is ambiguous, or purchase budget is exhausted.

- [ ] **Step 2: Run RED**

    node --test tests/production/production-verifier.test.ts tests/production/tke-deploy-workflow.test.ts tests/production/production-readiness.test.ts tests/production/production-manifest.test.ts

- [ ] **Step 3: Implement immutable mirror, read-only smoke, fixed-slot QA, and grouped rollback**

Pin source ghcr.io/gaofeng21cn/one-person-lab-webui@sha256:9d867fe0fc9db48b6efa27371d77770e46fc8cd97d26ef85a81fbdac7e96ca76, mirror to TCR tag 26.7.13, inspect target digest, and deploy repository@sha256 only. Ordinary smoke can read health/readiness/login/catalog/balance/existing Workspace and make one Gateway request, but cannot mutate resources. Fixed-slot QA may replace only test workload/Secret/data. Capture and restore the complete previous Cloud/App image set on failure.

- [ ] **Step 4: Run GREEN and commit**

    node --test tests/production/production-verifier.test.ts tests/production/tke-deploy-workflow.test.ts tests/production/production-readiness.test.ts tests/production/production-manifest.test.ts
    git add .github tools tests/production
    git commit -m "fix(release): reuse one prepaid verification slot"

## Phase 6: Integration, Real Verification, And Rollout

### Task 9: Merge, Review, Ship, Deploy, And Prove Production

**Files:**
- Merge-only in feat/launch-delivery-v2
- Update after real evidence: packages/contracts/opl-cloud-launch-freeze-contract.json
- Update after real evidence: docs/runtime/production-runbook.md

- [ ] **Step 1: Merge workers in dependency order**

    git merge --no-ff fix/fabric-prepaid-workspace
    git merge --no-ff feat/gateway-commercial
    git merge --no-ff feat/launch-ux-release

Re-run focused tests after each merge and resolve only actual overlapping hunks.

- [ ] **Step 2: Run complete local gate**

    npm test
    npm run typecheck
    npm run lint
    npm run build
    (cd services/internal/postgresmigrate && go test -race ./... -count=1)
    (cd services/control-plane && go test -race ./... -count=1)
    (cd services/fabric && go test -race ./... -count=1)
    (cd services/ledger && go test -race ./... -count=1)
    git diff --check origin/main...HEAD

- [ ] **Step 3: Run spec and code reviews, then ship one PR**

Fix every critical/high finding, rerun affected and complete gates, use the repository ship workflow, and wait for all GitHub CI jobs before merge.

- [ ] **Step 4: Build immutable Cloud and App images**

Build Cloud from the merge SHA. Mirror the verified App source digest. Record previous and candidate Cloud/Workspace image digests before deployment.

- [ ] **Step 5: Run read-only production inventory**

If exactly one compliant verification slot exists, reuse it. If none exists, run read-only PREPAID inventory/price checks and select cheapest compatible CVM plus minimum CBS. Multiple/ambiguous resources stop the process without purchase.

- [ ] **Step 6: Use the one purchase budget only if necessary**

Create exactly one PREPAID one-month manual-renew slot with stable idempotency/cost tags. Read back IDs/types/periods/renew flags/deadlines/zone/size/tags/NodePool/PV/PVC. Write non-secret IDs to the launch contract, set purchaseBudgetRemaining to 0, and commit/push before QA.

- [ ] **Step 7: Roll out and run real QA**

Verify three Deployment rollouts, health/readiness/login, Basic/Pro catalog, owner Key reveal, fixed-slot Workspace password login, WebSocket 101, one model response, and Key usage increase. Require identical CVM/CBS IDs before/after.

- [ ] **Step 8: Prove rollback and restore candidate**

Restore the previous image set, verify rollout/health/read-only smoke, redeploy candidate, and repeat checks. Never delete or repurchase the slot.

- [ ] **Step 9: Final cleanup and evidence**

Remove worker worktrees/local branches after integration, delete merged remote feature branches, confirm no stash, confirm main/integration state, and publish exact image/resource/CI/deploy references without secrets.
