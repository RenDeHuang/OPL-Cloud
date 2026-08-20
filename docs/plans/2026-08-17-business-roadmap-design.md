# OPL Cloud Business Roadmap Design

## Objective

把 `docs/roadmap.md` 扩展为一份统一的业务路线图，说明 OPL Cloud 从用户侧、运维侧、本地部署和云端 Instance 部署如何工作，并登记当前已发现的业务冲突与证据缺口。

## Canonical Owners

- 目标产品与权威边界：`docs/architecture.md`、`docs/decisions.md`
- 当前实现与运行证据：`docs/implementation-architecture.md`、源码、测试、`docs/status.md`
- 差距、优先级与验收：`docs/roadmap.md`
- 具体 medopl 云端部署：`opl-instance-medopl`

## Chosen Structure

保留现有结构性、安全、合同瘦身和简化 backlog，在其前面增加：

1. 业务总图与角色/权威边界；
2. 用户侧业务路线图；
3. 运维侧业务路线图；
4. 本地部署路线图；
5. 云端部署路线图；
6. 冲突与决策路线图。

每个路线条目均使用 `ID / 状态 / 优先级 / 当前行为或冲突 / owner / 验收证据`，并明确区分目标、当前实现、运行证据、生产证据和未知状态。

## Scope Decisions

- 只维护一份统一 roadmap，不拆成多个当前差距文档，避免形成多个 SSOT。
- 基础 Compose 控制服务不等于 local Workspace；两者分别列出。
- Cloud Release、Instance deployment、Workspace image 是三个不同生命周期，不合并。
- Launch Resume（故障 Launch 的受控继续）不等于用户 Workspace Resume（生命周期恢复）。
- 真实外部 Sub2API、clean-host 完整链路和 Instance 验收属于证据门槛，不以 fixture 或源代码存在替代。

## Acceptance Shape

路线图只记录业务差距和验收结果，不复制部署命令、运行手册、产品架构或当前状态快照。实现时优先修正文档中已确认的陈述陈旧项，并把确实影响用户或运维决策的冲突单列为可执行路线项。
