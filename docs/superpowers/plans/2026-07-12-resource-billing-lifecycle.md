# 资源计费生命周期实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让计算与存储资源具备可并发、可恢复、按资源 Hold 隔离的开通、小时结算、失败补偿和销毁释放闭环。

**Architecture:** Control Plane 先持久化稳定请求并让 Ledger 预留“7 天 + 首小时”；Fabric Pool Allocator 按规格汇总需求、统一调整 Node Pool 容量，并在 PostgreSQL 中原子建立 `resource_id -> machine_id` 归属。Machine 证据验证后 Ledger 才核销首小时；周期结算先用可用余额再用该资源 Hold，销毁停止新账期并只释放剩余 Hold。

**Tech Stack:** Go 1.22/1.24、Ent、PostgreSQL、腾讯云 TKE/CVM SDK、现有 HTTP 服务和 Node 测试。

---

### Task 1: Ledger Hold 生命周期合同

**Files:**
- Modify: `services/ledger/internal/ledger/types.go`
- Modify: `services/ledger/internal/ledger/store.go`
- Modify: `services/ledger/internal/ledger/store_test.go`
- Modify: `services/ledger/internal/http/server.go`
- Modify: `services/ledger/internal/http/server_test.go`

- [ ] **Step 1: 写失败测试**

增加测试证明：创建 Hold 预留 `sevenDays + firstHour`；`ActivateHold` 只核销首小时并留下 7 天；释放忽略调用方金额并释放 Ledger 计算的剩余值；结算必须提交 `holdId`。

```go
hold, _ := store.CreateHold(ctx, HoldInput{
    AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a",
    AmountCents: 16900, ActivationAmountCents: 100, Currency: "CNY",
    IdempotencyKey: "create",
})
active, _ := store.ActivateHold(ctx, HoldActivationInput{
    AccountID: "acct-a", ResourceType: "compute", ResourceID: "ca-a",
    HoldID: hold.ID, ProviderEvidenceRef: "fabric:op-a", IdempotencyKey: "activate",
})
if active.RemainingCents != 16800 || active.ConsumedCents != 100 {
    t.Fatalf("activation = %#v", active)
}
```

- [ ] **Step 2: 验证测试失败**

Run: `cd services/ledger && go test ./internal/ledger ./internal/http -run 'Test(HoldActivation|ReleaseRemainingHold|SettlementRequiresOwningHold)' -count=1`

Expected: FAIL，因为激活合同和 Hold 剩余状态尚不存在。

- [ ] **Step 3: 增加最小合同**

为 `HoldInput` 增加 `ActivationAmountCents`；为 Hold 结果增加 `OriginalCents`、`RemainingCents`、`ConsumedCents`、`ReleasedCents`；新增 `HoldActivationInput/Result` 和 `ActivateHold` Store 方法；为 `ResourceSettlementInput` 增加 `HoldID`；从 `HoldReleaseInput` 删除资金权威意义上的 `AmountCents`。

- [ ] **Step 4: 增加 HTTP 路由**

新增 `POST /ledger/holds/activate`，沿用 `Idempotency-Key` 头；更新 release 与 settlement 错误映射，使身份不匹配返回 400、余额或 Hold 不足返回 409。

- [ ] **Step 5: 运行聚焦测试**

Run: `cd services/ledger && go test ./internal/ledger ./internal/http -run 'Test(HoldActivation|ReleaseRemainingHold|SettlementRequiresOwningHold)' -count=1`

Expected: 编译通过，Store 实现测试仍因状态逻辑缺失而失败。

### Task 2: Ledger 内存 Store 的正确资金算法

**Files:**
- Modify: `services/ledger/internal/ledger/memory_store.go`
- Modify: `services/ledger/internal/ledger/store_test.go`

- [ ] **Step 1: 写可用余额优先和自身 Hold 兜底测试**

构造两个资源 Hold，结算资源 A 时先使用可用余额，不足部分只减少 Hold A；Hold B 完全不变。再验证可用余额加 Hold A 不足时整笔失败，钱包和两个 Hold 都不变化。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd services/ledger && go test ./internal/ledger -run 'TestSettlement(UsesAvailableBeforeOwningHold|NeverConsumesAnotherHold|FailsAtomically)' -count=1`

Expected: FAIL，当前实现减少账户全局 frozen。

- [ ] **Step 3: 实现内存状态转换**

在同一 mutex 临界区内校验 Hold 归属和状态，执行：

```go
availablePart := minInt64(input.AmountCents, wallet.AvailableCents)
holdPart := input.AmountCents - availablePart
if holdPart > hold.RemainingCents {
    return ResourceSettlementResult{}, ErrInsufficientResourceHold
}
wallet.BalanceCents -= input.AmountCents
wallet.FrozenCents -= holdPart
hold.RemainingCents -= holdPart
hold.ConsumedCents += holdPart
wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
```

`ActivateHold` 从该 Hold 同时减少 balance、frozen 和 remaining，增加 spent 与 consumed；`ReleaseHold` 只减少 frozen 和 remaining，不改变 balance。

- [ ] **Step 4: 运行 Ledger 内存测试**

Run: `cd services/ledger && go test ./internal/ledger -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add services/ledger/internal/ledger services/ledger/internal/http
git commit -m "feat(ledger): enforce per-hold lifecycle"
```

### Task 3: Ledger PostgreSQL 状态、迁移和并发锁

**Files:**
- Modify: `services/ledger/ent/schema/hold.go`
- Modify: `services/ledger/ent/schema/hold_release.go`
- Modify: `services/ledger/ent/schema/resource_settlement.go`
- Create: `services/ledger/ent/schema/hold_activation.go`
- Create: `services/ledger/migrations/202607120001_resource_hold_lifecycle.sql`
- Create: `services/ledger/internal/ledger/ent_migrations/202607120001_resource_hold_lifecycle.sql`
- Modify: `services/ledger/internal/ledger/postgres_store.go`
- Modify: `services/ledger/internal/ledger/postgres_store_test.go`
- Regenerate: `services/ledger/ent/**`

- [ ] **Step 1: 写 PostgreSQL 并发测试**

两个 goroutine 同时结算或释放同一 Hold，断言最多一条状态转换成功且不出现负余额、负 frozen 或超额 consumed/released。

- [ ] **Step 2: 增加 schema 和向前迁移**

Hold 增加 `original_cents`、`activation_cents`、`remaining_cents`、`consumed_cents`、`released_cents`、`provider_evidence_ref`；settlement 增加 `hold_id`；activation 表以幂等键唯一。历史 Hold 保守回填为原始金额全部 remaining，歧义数据保持 reserved 等待对账。

- [ ] **Step 3: 重新生成 Ent**

Run: `cd services/ledger && go run entgo.io/ent/cmd/ent generate ./ent/schema`

Expected: 新增 HoldActivation 生成代码，现有生成代码与 schema 一致。

- [ ] **Step 4: 实现 PostgreSQL 锁定顺序**

所有资金变更在一个事务中先执行钱包 `SELECT ... FOR UPDATE`，再对指定 Hold 执行 `SELECT ... FOR UPDATE`。激活、结算、释放均在锁内验证资源身份、币种、状态和幂等哈希，再写 LedgerEntry、WalletTransaction 和业务记录。

- [ ] **Step 5: 运行 PostgreSQL 与完整 Ledger 测试**

Run: `cd services/ledger && go test ./... -count=1`

Expected: PASS；没有负数或重复扣款。

- [ ] **Step 6: 提交**

```bash
git add services/ledger
git commit -m "feat(ledger): serialize resource hold accounting"
```

### Task 4: Fabric Machine ownership

**Files:**
- Create: `services/fabric/ent/schema/machine_ownership.go`
- Create: `services/fabric/migrations/202607120001_machine_ownership.sql`
- Create: `services/fabric/internal/fabric/ent_migrations/202607120001_machine_ownership.sql`
- Modify: `services/fabric/internal/fabric/types.go`
- Modify: `services/fabric/internal/fabric/operation_store.go`
- Modify: `services/fabric/internal/fabric/operation_store_test.go`
- Regenerate: `services/fabric/ent/**`

- [ ] **Step 1: 写并发认领测试**

创建 100 条 pending resource 和 100 台 Ready Machine，并发运行认领；断言 100 个不同 `resource_id` 对应 100 个不同 `machine_id`。重复 Machine 或重复 active resource 必须返回归属冲突。

- [ ] **Step 2: 定义 ownership 合同**

```go
type MachineOwnership struct {
    ID, ResourceID, AccountID, WorkspaceID string
    NodePoolID, MachineID, InstanceID, NodeName string
    Status string
    ClaimedAt time.Time
    ReleasedAt time.Time
}
```

OperationStore 增加列出 pending demand、原子 claim、激活、隔离和释放 ownership 的方法；Memory 与 PostgreSQL 实现保持一致。

- [ ] **Step 3: 增加唯一约束和迁移**

数据库唯一约束 MachineID、非空 InstanceID，以及 claimed/active 状态下 ResourceID 的唯一归属。旧资源只在身份唯一时回填，否则不自动认领。

- [ ] **Step 4: 重新生成 Ent 并运行测试**

Run: `cd services/fabric && go run entgo.io/ent/cmd/ent generate ./ent/schema && go test ./internal/fabric -run 'TestMachineOwnership' -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add services/fabric
git commit -m "feat(fabric): persist unique machine ownership"
```

### Task 5: Fabric Node Pool Allocator

**Files:**
- Create: `services/fabric/internal/fabric/pool_allocator.go`
- Create: `services/fabric/internal/fabric/pool_allocator_test.go`
- Modify: `services/fabric/internal/fabric/service.go`
- Modify: `services/fabric/internal/fabric/types.go`
- Modify: `services/fabric/internal/fabric/tencent_provider.go`
- Modify: `services/fabric/internal/fabric/tencent_provider_test.go`
- Modify: `services/fabric/cmd/opl-tencent-provisioner/main.go`
- Modify: `services/fabric/cmd/opl-tencent-provisioner/main_test.go`
- Modify: `services/fabric/internal/http/server.go`
- Modify: `services/fabric/internal/http/server_test.go`

- [ ] **Step 1: 写目标容量与批量分配测试**

测试同规格 100 个 pending demand 计算 `desired=100`，Provider 只接收池级目标容量；50 个 active、20 个 pending、10 个 destroying 计算最新净需求；单个 Machine 失败不会让其他资源失败。

- [ ] **Step 2: 拆分 Provider 池级能力**

将逐请求 `CreateComputeAllocation` 的扩容职责替换为：

```go
ReconcileComputePool(ctx context.Context, input ComputePoolDemand) (ComputePoolState, error)
TagComputeMachine(ctx context.Context, machine ProviderMachine, ownership MachineOwnership) error
DeleteComputeMachine(ctx context.Context, machine ProviderMachine) error
```

`ComputePoolState` 返回 NodePoolID、目标副本数和全量 Ready Machine 身份。腾讯 `ScaleNodePool` RequestId 只写入 operation provider evidence。

- [ ] **Step 3: 实现 Allocator**

Allocator 按 `cluster + package + instanceType` 获取 PostgreSQL advisory lock，重新读取 pending/claimed/active 需求，调用池级 reconcile，然后通过 OperationStore 原子认领未归属 Ready Machine。锁内不使用调用前缓存的 replica 数。

- [ ] **Step 4: 标签与复核**

认领后把 CVM 实例名写为 `resource_id` 并回读确认，再写入包含 `resource_id`、`account_id`、`workspace_id` 的 Node Label；全部一致才将 ownership 变为 active。失败进入 quarantined，禁止返回 running。

- [ ] **Step 5: 清退旧算法**

删除 `finishComputeAllocation` 的每请求扩容 goroutine、`waitForNewPoolMachine` 与 `selectNewReadyMachine` 的逐请求前后差分认领，以及本地伪 RequestId 作为成功证据的路径。保留真实腾讯 RequestId 作为追踪证据。

- [ ] **Step 6: 严格删除和存储验证**

Compute 销毁按 active ownership 精确删除并确认 Machine/Node/Instance 不存在；Storage 创建等待 PVC Bound，销毁传播 kubectl 错误并确认 PVC 不存在。

- [ ] **Step 7: 运行 Fabric 测试**

Run: `cd services/fabric && go test ./... -count=1`

Expected: PASS，包含 100 并发认领、Provider 部分失败和严格删除。

- [ ] **Step 8: 提交**

```bash
git add services/fabric
git commit -m "feat(fabric): allocate concurrent node pool capacity"
```

### Task 6: Control Plane 激活、失败和销毁 Saga

**Files:**
- Modify: `services/control-plane/internal/clients/ledger.go`
- Modify: `services/control-plane/internal/clients/ledger_test.go`
- Modify: `services/control-plane/internal/clients/fabric.go`
- Modify: `services/control-plane/internal/clients/fabric_test.go`
- Modify: `services/control-plane/internal/controlplane/service.go`
- Modify: `services/control-plane/internal/controlplane/service_test.go`
- Modify: `services/control-plane/internal/server/routes_resources.go`
- Modify: `services/control-plane/internal/server/provider_reconcile_worker.go`
- Modify: `services/control-plane/internal/server/settlement_worker.go`
- Modify: `services/control-plane/internal/server/settlement_worker_test.go`
- Modify: `services/control-plane/internal/server/server_test.go`

- [ ] **Step 1: 写 Saga 失败注入测试**

覆盖 Hold 预留失败、Fabric pending 保存失败、Machine 验证失败、Ledger 激活失败、激活成功后投影失败、销毁 Provider 失败、Ledger 释放后投影失败。每个重试都必须收敛到一台 Machine、一笔首小时扣款和一份 Hold。

- [ ] **Step 2: 稳定请求身份**

复用 ExecutionRequest claim，让 `request_id` 和 `resource_id` 从幂等键稳定派生。同步 HTTP 错误也保存 failed/stopped 投影，不能留下永久 provisioning。

- [ ] **Step 3: 接入预留与激活**

价格预览计算 `holdAmount = sevenDays + firstHour` 和 `activationAmount = firstHour`。Create 先预留，再提交 Fabric pending；provider reconcile 发现 ownership active 且证据完整后调用 Ledger ActivateHold，最后保存 billing active。

- [ ] **Step 4: 接入周期结算**

`ResourceSettlementInput` 必须携带资源自身 `holdId`。billable 只允许 provider 证据新鲜且 billing active 的资源；Ledger 返回资源 Hold 不足时把 billing 改为 stopping，并以稳定幂等键触发销毁，同时继续处理其他资源。

- [ ] **Step 5: 接入销毁与释放**

接受销毁请求后先持久化 billing stopping，Fabric 确认 ownership released 或 PVC 不存在后再调用 Ledger ReleaseHold。删除调用方 `HoldAmountCents` 作为释放金额权威的路径，保存 Ledger 返回的实际 release amount。

- [ ] **Step 6: 运行 Control Plane 测试**

Run: `cd services/control-plane && go test ./internal/clients ./internal/controlplane ./internal/server -count=1`

Expected: PASS。

- [ ] **Step 7: 提交**

```bash
git add services/control-plane
git commit -m "feat(control-plane): coordinate resource billing lifecycle"
```

### Task 7: Console 与合同状态

**Files:**
- Modify: `apps/console-ui/src/store/console-state.ts`
- Modify: `apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx`
- Modify: `packages/contracts/opl-cloud-route-api-contract.json`
- Modify: `tests/ui/commercial-console-surface.test.ts`
- Modify: `tests/contracts/route-api-contract.test.ts`

- [ ] **Step 1: 定位并测试现有资源页面**

在现有 `ResourceProvisioningPages.tsx` 增加合同测试，要求展示后端 `provisioning/running/destroying/failed/destroyed` 和 `pending/active/stopping/stopped`，不创建重复页面，不在前端推算余额或 Hold。

- [ ] **Step 2: 实现最小状态展示**

复用现有轮询和商业资源组件，仅补充缺失的 stopping、quarantined/failed 客户安全状态和后端 receipt 字段。

- [ ] **Step 3: 运行 Node 测试和类型检查**

Run: `npm test && npm run typecheck && npm run build`

Expected: PASS。

- [ ] **Step 4: 提交**

```bash
git add apps/console-ui packages/contracts tests
git commit -m "feat(console): show reconciled resource billing states"
```

### Task 8: 迁移、回归与 rollout 验证

**Files:**
- Modify: `tools/production-verifier.ts`
- Modify: `tests/production/production-verifier.test.ts`
- Modify: `docs/runtime/production-runbook.md`

- [ ] **Step 1: 增加闭环 verifier**

验证器必须检查：资源返回稳定 request/resource ID；Machine ownership 证据唯一；成功只有一笔首小时 settlement；失败无 debit 且 Hold 全释放；销毁后 billing stopped、后续账期不再增加、Hold remainder 为零。

- [ ] **Step 2: 运行所有本地测试**

```bash
npm test
npm run typecheck
npm run build
cd services/ledger && go test ./... -count=1
cd ../fabric && go test ./... -count=1
cd ../control-plane && go test ./... -count=1
```

Expected: 全部 PASS。

- [ ] **Step 3: 运行并发和竞态测试**

```bash
cd services/ledger && go test -race ./internal/ledger -count=1
cd ../fabric && go test -race ./internal/fabric -run 'Test(MachineOwnership|PoolAllocator)' -count=1
cd ../control-plane && go test -race ./internal/controlplane ./internal/server -run 'Test(Resource|PeriodicSettlement|ProviderReconcile)' -count=1
```

Expected: PASS，无 data race。

- [ ] **Step 4: 运行 rollout 前验证**

Run: `npm run validate:production-manifest && npm run verify:production -- --help`

Expected: manifest 合同和 verifier 入口有效。若环境提供生产 URL、凭据和显式付费确认，再执行真实 staging/production resource E2E；缺少任一项时记录未执行原因，不伪造通过。

- [ ] **Step 5: 更新运行手册并提交**

```bash
git add tools tests docs/runtime
git commit -m "test: verify resource billing rollout"
```

### Task 9: 审查、合并、推送和清理

**Files:**
- Review: 本分支相对 `main` 的全部 diff

- [ ] **Step 1: 检查工作区和临时文件**

Run: `git status --short && git ls-files --others --exclude-standard`

Expected: 没有未提交文件、日志、测试数据库、截图或临时计划。

- [ ] **Step 2: 运行最终 diff 审查**

检查 SQL 向前兼容、资金不变量、错误路径、幂等冲突和敏感 Provider 数据；确认旧算法已删除且历史迁移未修改。

- [ ] **Step 3: 合并最新 main**

在功能 worktree 中合并最新 `main`，解决冲突后重跑 Task 8 全部验证。

- [ ] **Step 4: 合并到 main 并 push**

在主 worktree 执行非强制 fast-forward/merge，确认远端没有新提交后 push。禁止 force push。

- [ ] **Step 5: rollout 后检查**

使用仓库现有 deploy/canary 能力检查服务 readiness、Console 状态、Ledger settlement 与 Hold release 证据。任何失败立即停止 rollout 并保留可诊断信息。

- [ ] **Step 6: 清理 worktree**

确认分支已经合并和 push 后，从主 worktree执行：

```bash
git worktree remove .worktrees/resource-billing-lifecycle
git branch -d feature/resource-billing-lifecycle
git worktree prune
```

Expected: worktree 和已合并本地功能分支清理完成，主工作区干净。
