# OPL Cloud Business Roadmap Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 在唯一的 `docs/roadmap.md` 中建立用户侧、运维侧、本地部署、云端部署和冲突处理五个业务视图。

**Architecture:** 不改变 `docs/architecture.md`、`docs/status.md` 等权威分层，只把目标与当前证据之间的业务差距投影为可执行路线项。保留现有产品、结构、安全、合同和简化 backlog，并用稳定 ID 交叉引用，避免产生第二套 roadmap。

**Tech Stack:** Markdown、Git、现有文档生命周期与仓库验证脚本。

---

### Task 1: Add Business Views

**Files:**
- Modify: `docs/roadmap.md`

**Step 1: Add the business model and authority map**

在 `Planning Semantics` 后加入业务总图，明确 Console、Control Plane、Fabric、Ledger、Sub2API、Workspace image owner 和 Instance owner 的职责。

**Step 2: Add four roadmap views**

加入用户侧、运维侧、本地部署和云端部署表格。每行必须引用稳定 gap ID，并说明当前行为、owner 与验收证据。

**Step 3: Keep evidence levels explicit**

明确区分源代码存在、fixture/CI、live clean-host、Instance deployment 和 production-proven，禁止从低层证据推导高层 readiness。

### Task 2: Register And Reconcile Conflicts

**Files:**
- Modify: `docs/roadmap.md`

**Step 1: Add the conflict table**

登记 Basic/Pro 展示与准入、余额相等边界、续费不可达、Launch Resume 与 Workspace Resume 混淆、删除留存、基础 Compose 与 local Workspace profile、候选发布顺序、旧 Evidence Gaps 陈述等冲突。

**Step 2: Convert actionable conflicts into gap rows**

为用户可见套餐与余额边界、续费/恢复、生命周期语义增加独立稳定 ID；复用现有 `PRODUCT-RELEASE-01`、`MVP-LOCAL-WORKSPACE-GATEWAY-01` 和 `INSTANCE-MEDOPL-01`，不复制 owner。

**Step 3: Reconcile stale prose**

把“缺 local profile Release”和“缺首个 Instance receipt”改为真实剩余差距：clean-host/live 资格证据，以及 Runtime readiness/Acceptance B/rollback 证据。

### Task 3: Verify Documentation Integrity

**Files:**
- Verify: `docs/roadmap.md`
- Verify: `docs/plans/2026-08-17-business-roadmap-design.md`
- Verify: `docs/plans/2026-08-17-business-roadmap.md`

**Step 1: Inspect the diff**

Run: `git diff --check && git diff -- docs/roadmap.md docs/plans/2026-08-17-business-roadmap.md`

Expected: no whitespace errors; only the approved roadmap and plan projection changes.

**Step 2: Run the repository's ordinary local gate**

Run: `npm run verify:local`

Expected: PASS; this is the repository's actual source/documentation boundary gate. If an unrelated environment dependency blocks it, report the exact blocker without weakening the roadmap acceptance.

**Step 3: Commit**

```bash
git add docs/roadmap.md docs/plans/2026-08-17-business-roadmap.md
git commit -m "docs: map cloud business and deployment roadmaps"
```
