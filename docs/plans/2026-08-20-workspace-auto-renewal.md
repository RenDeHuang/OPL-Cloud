# Workspace 客户自助自动续费实施计划

Owner: `one-person-lab-cloud`
Lane: `opl-tnw.2.4` (B4)
Primary module: `services/control-plane`
Canonical contract owners: Control Plane Workspace billing state and
`packages/contracts/opl-cloud-billing-ledger-contract.json`; Console DTO
projection remains owned by `apps/console-ui`.
Base: `origin/main` at `707a9c117044b42cb051148e6396c5082c93be4d`

## Objective

让客户可以针对每个独立 Workspace 自主开启或关闭自动续费。续费保持
Control Plane 现有持久化 renewal state machine：按月预付、每个 Workspace
每个 `paidThrough` 账期最多一个确认扣款，使用同一个稳定 operation/debit/
receipt 身份，并通过已有 Fabric readback、Sub2API wallet 和 Ledger receipt
路径完成结算。到期未付仍然拒绝访问并关闭续费意图，不执行 Fabric/provider
停止、删除或第二次购买。

## Current evidence and gap

- `workspace_renewal.go` 已拥有持久化 intent、CAS、fenced lease、renewal
  operation、单次 debit、provider readback、receipt-only retry、manual review
  和过期状态机。
- `POST /api/workspaces/{workspaceId}/auto-renew` 已具备 owner、CSRF、幂等
  key、审计和 CAS 语义，但当前对 `autoRenew=true` 返回
  `autoRenew_unavailable`。
- `POST /api/workspace-launches` 同样拒绝 `autoRenew=true`，且 activation
  当前把已接受的 launch intent 强制写回 `autoRenew=false`。
- Console 只能展示自动续费状态；Launch DTO 将 `autoRenew` 固定为 `false`，
  详情页没有客户开关。
- 当前产品/合同仍写着 `hidden_until_real_renewal_evidence`；B4 将把它改为
  客户可控的合同事实，并以 focused/full local evidence 证明，不声称生产或
  Instance 续费已发生。

## Exact write set

1. `services/control-plane/internal/server/routes_workspace_launch.go`
   - 移除客户 launch 对 `autoRenew=true` 的拒绝；保留显式布尔校验、报价、
     资格、余额、幂等和现有单 Workspace launch 边界。
2. `services/control-plane/internal/server/routes_workspace.go`
   - 移除 customer auto-renew enable 的不可用分支；保留 owner/tenant、CSRF、
     mutation key、CAS、审计、过期 reactivation fail-closed 和 replay 语义。
3. `services/control-plane/internal/server/workspace_launch_activation.go`
   - 从持久化 launch operation 读取并保留 `autoRenew` 与授权 actor/time，
     不创建第二份续费状态。
4. Control Plane focused Go tests（现有 launch/renewal tests 所在文件）
   - 覆盖 launch 启用后 activation/readback 保留 intent；客户按 Workspace
     开关 enable/disable；同一账期并发 worker 只有一个 claim/debit/receipt；
     exact replay 不重复扣款；余额不足、provider unknown、receipt retry 和
     unpaid expiry 仍按现有状态机收敛。
5. `apps/console-ui/src/api/dtos.ts`、
   `apps/console-ui/src/pages/CustomerPages.tsx`、
   `apps/console-ui/src/app/use-console-controller.ts`
   及其必要的 API/UI focused tests
   - 将 launch/response 的 `autoRenew` 改为布尔值；提供 Workspace 级开关，
     调用既有 `/auto-renew` API 并回读服务端 response；不在浏览器计算价格、
     账期、余额或续费状态。
6. `packages/contracts/opl-cloud-product-contract.json`、
   `packages/contracts/opl-cloud-billing-ledger-contract.json` 及对应合同测试
   - 将 customer renewal control 从隐藏改为 Control Plane Workspace 级客户
     授权；明确一次月度 period 只能确认一次 Workspace debit，保持 Sub2API
     唯一钱包、Ledger append-only receipt 和 Fabric provider-neutral 边界。
7. `docs/status.md`、`docs/roadmap.md`
   - 仅在实现与 gates 完成后回写当前证据和 `WORKSPACE-RENEWAL-REACTIVATION-01`
     的剩余边界；不写入生产、真实购买、Instance qualification 或 release
     结论。

## Explicit non-scope and lane boundaries

- B1/B2/B3：不改账户购买资格、价格 catalog、provider profile 或 launch
  admission policy。
- B5：不改 Workspace delete、Suspend/Resume、provider reclamation 或资源
  owner 语义；到期仍为 zero Fabric/provider mutation。
- B6：不改 Ledger receipt schema owner、Ledger tables 或第二 wallet；本 lane
  只复用现有 public clients 和 receipt contract。
- B7：不改 Release、Instance deployment、production environment、Secrets、
  TKE/CVM/CBS 真实购买/续费或私网访问。
- 不把一次购买拆成多个 Workspace，不增加第二 Workspace service、wallet、
  event bus、workflow runtime 或跨服务 source/table import。

## Acceptance

- 一个客户账户可为 Workspace A 开启、为 Workspace B 关闭，彼此状态和账期
  独立；Workspace 数量仍为 `0..N`。
- Launch `autoRenew=true` 的 intent 在 Workspace activation/readback 中保持，
  并进入同一持久化 renewal machine。
- 同一 Workspace、同一 `paidThrough` 的并发 worker/replay 最多一个 confirmed
  Sub2API debit、一个 provider renewal/readback chain、一个 Ledger receipt。
- 失败、未知、manual review、receipt-only retry、余额不足和 unpaid expiry
  不会产生第二 debit、第二 provider mutation、refund 猜测或隐式 reactivation。
- Console 只调用 Control Plane product API，显示服务端 authoritative state；
  focused checks、`npm run verify:local` 和 `npm run verify:local:full` 通过。
