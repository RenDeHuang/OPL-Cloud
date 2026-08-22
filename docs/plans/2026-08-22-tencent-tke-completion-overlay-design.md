# Tencent/TKE 完成度覆盖图设计

Owner: `one-person-lab-cloud`
Purpose: `approved_supporting_completion_overlay_design`
State: `approved_design`

## Objective

在已确认的 OPL Cloud Workspace 目标架构图之上，增加一组只读的
Tencent/TKE 完成度覆盖图，回答以下问题：

1. 从客户购买到 TKE Workspace 可访问，当前每个节点完成到了哪一层证据；
2. Control Plane、Fabric、Tencent provider、Sub2API、Ledger 和 Instance
   owner 分别负责什么；
3. 跨 Owner 链路先持久化什么、何时允许外部写、失败后从哪里恢复；
4. 当前 Candidate 还缺什么证据才能成为正式 OPL Cloud Release。

固定 Input 仍是“客户需要 OPL App 的云端产品”，固定 Output 仍是
“可移植、可治理、可追溯并可由实例部署的 OPL Cloud 产品”。Tencent/TKE
是 medopl Instance 选择的 Fabric extension，不是新的产品边界。

## Canonical Owners

完成度图不成为第二个 SSOT：

- `docs/architecture.md`、`docs/decisions.md`：目标架构和长期决策；
- `docs/implementation-architecture.md`、source、schemas、tests：当前实现；
- `docs/status.md`：当前证据；
- `docs/roadmap.md`：开放缺口和验收；
- `opl-instance-medopl`：TKE 环境、Provider Profile、Secrets、部署、回滚、
  运行回读和 Instance receipt。

图中若 `docs/status.md` 与 `docs/roadmap.md` 对同一当前事实冲突，必须先按
源码、focused tests 和 Git history 复核，再由 canonical owner 消除重复当前
写者，支持性图稿不得自行保留另一套结论。

## Evidence Levels

图上不使用主观百分比。每个节点使用以下证据等级：

| Level | Meaning |
| --- | --- |
| `TARGET` | 只有目标定义，未声明当前实现 |
| `SOURCE` | 当前源码、schema 和 focused tests 已实现 |
| `LOCAL` | 有本地真实运行证据，但必须注明是否为 exact-current Candidate |
| `CANDIDATE` | 已绑定精确 SHA 和 multi-architecture digest 完成资格验收 |
| `INSTANCE` | Instance owner 已部署并完成权威运行回读 |
| `RELEASED` | 同一已验收字节已正式发布 |
| `BLOCKED` | 存在明确 Gap 或缺少上层 owner 回执 |
| `LATER` | 不属于当前 Workspace Release |

`SOURCE + BLOCKED` 表示实现存在，但目标上层证据尚未取得。低层证据不能
自动提升为 `INSTANCE` 或 `RELEASED`。

## Owner Responsibilities

| Owner | Responsibility | Durable write set |
| --- | --- | --- |
| Console | 展示 Package、提交客户命令、轮询原 Operation | 无数据库，只调用 Control Plane |
| Control Plane | Session、报价/准入、Workspace Launch/Delete/Reconcile、客户投影 | 自有 Workspace、Runtime Operation、对账 Guard |
| Sub2API | Identity、Wallet、Workspace Key、routing、Usage | 外部权威库；Cloud 只保存 ref/readback |
| Fabric | provider-neutral stage contract、资源身份、mutation/readback | 自有 Fabric Operation、Machine Ownership |
| Tencent provider adapter | 将 Fabric port 映射到 CVM/TKE/CBS/Kubernetes 并做权威回读 | Provider 资源；provider state 由 Fabric 记录 |
| Ledger | purchase/delete receipt、Evidence Index、reconciliation、idempotency | 自有 append-only receipt/evidence tables |
| `opl-instance-medopl` | TKE Profile、domain、Workspace image、Secrets、部署、回滚、receipt | Instance 仓库和受保护环境 |
| Cloud Release owner | 构建 Candidate、校验双路径回执、提升原字节 | Candidate/Release manifest 和公开资产 |

## Persistence Model

Workspace Launch 是跨限界上下文 Saga，不使用分布式事务：

1. Control Plane 先持久化一个业务 Operation，保存原始业务意图、当前
   stage、attempt/lease/CAS、观察结果和授权；
2. 每个 Fabric stage 携带不可变的 Launch/Account/Workspace/stage/action、
   operation ID、idempotency key、request hash 和 expected binding；
3. Fabric 在任何 Tencent provider 写入前 claim 并持久化 Fabric Operation；
4. Tencent adapter 执行有界 mutation，并从 CVM/TKE/CBS/Kubernetes 做
   权威 readback；
5. Fabric 保存 provider state 和资源 ref 后返回 typed observation；
6. Control Plane 通过 CAS 推进同一个业务 Operation；
7. Activation 原子写 Control Plane Workspace projection；
8. Ledger 以独立 idempotency key 和 request hash 追加 Receipt，Control
   Plane 将 Receipt ID 保存到原 Launch Operation；当前尚未同步到 Workspace
   `purchase_receipt_id` 读模型投影。

恢复规则是 `readback -> decide -> reserve -> mutate -> readback -> persist`。
`pending` 只允许有限次、预先持久化的 continuation read；`unknown`、身份
漂移或预算耗尽进入 `manual_review`，不得通过新资源或第二次扣款补救。

Delete 使用另一个持久化 Control Plane Operation，并按 owner 权威 absence
顺序收敛：`Runtime + Secret -> Attachment -> Storage -> Compute -> Key ->
Workspace projection -> workspace.deleted.v1 Receipt`。Delete 不退款，也不
修改 wallet。

## Diagram Set

保留 01-13 为目标架构与业务链图，新增：

| No. | Name | Question answered |
| --- | --- | --- |
| 14 | Tencent/TKE 产品链完成度与 Owner 总览 | 哪些能力已到源码层，哪些仍被 Instance/Release 证据阻断 |
| 15 | Tencent/TKE Workspace Launch 持久化业务链 | 每一步谁先写什么、谁调用谁、何时允许推进 |
| 16 | Tencent/TKE 恢复、删除与对账收敛 | 未知结果、崩溃、删除和跨 Owner 对账如何收敛 |
| 17 | Tencent/TKE Candidate 到 Release 证据链 | 同一 SHA/digest 如何经过 Local、Instance 和 exact-byte promotion |

每个主要节点包含 `Owner`、`职责`、`状态`、`Gap` 和 `Next Evidence`。

## Current Assessment

- 业务/模块边界合理：Control Plane 是业务 process manager，Fabric 是资源
  fulfillment context，Tencent adapter 是 provider ACL，Ledger 只保存证据；
- 数据边界合理：三个服务独立 PostgreSQL owner，不跨库读写，不建立共享
  总库；
- 持久化顺序合理：Control Plane 和 Fabric 都在各自外部 mutation 前保存
  identity/idempotency binding，并以 owner readback 推进；
- DDD/整洁架构方向正确：依赖指向 typed ports，provider 细节不进入客户
  DTO，Sub2API 不被复制成第二钱包；
- 当前关键风险是证据闭环而非主链缺失：尚无同一 current Candidate 的 TKE
  正常购买、Runtime/provider/billing readback、执行回滚和
  `workspace_verified` receipt；
- `PORTABLE-INSTALL-01` 仍需删除 medopl domain/image fallback，并让安装 owner
  显式提供 Provider Profile、domain 和 immutable Workspace image；
- `PRODUCT-RELEASE-01` 仍需将 formal Release 从 rebuild 改为提升已验收的
  exact bytes；
- Activation 与 Purchase Receipt 分属 Control Plane 和 Ledger 数据库，存在
  合法但必须观测的一致性窗口：Workspace projection 可能已经 running，Receipt
  尚未确认。恢复只能追加原 Receipt，不得重复 Debit、资源 mutation 或
  Activation；当前 Receipt 成功后会把 ID 保存到原 Launch Operation，但还未
  回写 Workspace `purchase_receipt_id` projection，导致 Billing/Admin 读模型
  可能漏掉 Receipt ref。Permanent Delete 不依赖该投影，而是用唯一 succeeded
  Launch Operation 的 `receiptId` 向 Ledger 做精确校验。该问题是 P1 投影完整性
  Gap，不是 Delete 或当前 Release 的 P0 blocker；
- typed Tencent Launch 的 Compute 路径具备 parent/child operation、mutation
  journal、ownership CAS 和 provider readback，但未单独证明同 NodePool 的并发
  Launch 不会重复扩容。当前 Pilot 的有限并发不能替代扩容后的验收；
- Kubernetes PV/PVC、Secret、Runtime 多对象 apply 发生 partial state 时会
  fail closed 到 `manual_review`。Instance 运维链必须证明如何基于 exact owner
  identity 处理残留，不能采用模糊批量清理；
- `docs/status.md` 的 Basic/Pro 和 exact-balance 描述已由当前 Console
  `>=` 判断、Console focused test 和 Control Plane Basic/Pro exact-balance
  admission test 证实；陈旧的 `CONSOLE-LAUNCH-CONSISTENCY-01` 已从 canonical
  Roadmap 移除。

## Completion Evidence

本图稿包完成时应满足：

- 四张 Mermaid 源图可以由固定 Mermaid 版本渲染；
- 四张 SVG 与源图同名，标题清楚说明图片内容；
- 图中每个状态均可追溯到当前 source、`docs/status.md` 或
  `docs/roadmap.md`；
- 图不声称已执行 TKE 部署、真实腾讯资源 mutation 或正式 Release；
- `git diff --check`、Mermaid parse/render、SVG XML 检查和
  `npm run verify:local` 通过。
