# OPL Cloud Pilot V2 工程实施计划

> **状态：owner 已批准本地 code-complete 执行。** 本文定义实施顺序、原子提交和验收门禁；
> 未经再次批准仍不得执行真实写入、Acceptance、部署、push、PR 或 merge。

> **实施纪律：** 每个切片在独立 clean worktree 中完成：先实际运行新增测试取得 RED，
> 再做最小实现，focused PASS 后运行该切片列出的受影响 module 回归，形成一个原子 commit；
> integration owner 只 cherry-pick 通过的 commit。故意失败的测试状态不提交到 integration 分支。
> 第 8.1 节最终 integration 全量只在 S14 对最终 HEAD 运行一次。

**目标：** 以 owner 批准输入 `73c0ec09f3eff301cb4df842fda804c26ed37120` 及最终冻结 delta
`c313f59a72b8a69c9ef7c5bdc4ab12c610d76a37`
作为唯一设计输入，把当前 Paid Pilot hard cut 为 2-5 人 invite-only Pilot V2：一个 Sub2API
钱包真相、通用 Key、保留 Workspace Key、按 Key Usage、一次 Workspace 总额扣款、真实 Runtime
文件摘要、完整用户/运维体验和最小公告；不创建竞争真相。

**架构：** Console 浏览器只访问 Control Plane 产品 API。Control Plane 拥有 Session、账号映射、
权限、命名 DTO、业务操作和状态收敛；Sub2API 拥有余额、Key、模型和 Usage；Fabric 是腾讯、
Kubernetes、Runtime 与 Secret 的唯一写入方；Runtime 从已挂载 `/projects` 读取文件事实；Ledger
只追加非秘密证据。Pilot 不建只读副本，也不直连 Sub2API PostgreSQL。

**技术栈：** Go 1.22、Ent/PostgreSQL 16、`net/http`、Vue 3/TypeScript、Node 24 test runner、
Playwright 1.61、Kubernetes/TKE、Tencent CVM/CBS、Sub2API、Sentrux。

---

## 1. 冻结输入与调查基线

### 1.1 Revision pins

| 对象 | Revision | 用法 |
| --- | --- | --- |
| clean main | `833599755a05d222683ee81fc74791eb549cda26` | 工程分支起点，不修改 |
| Pilot V2 owner 批准输入 | `73c0ec09f3eff301cb4df842fda804c26ed37120` | 与工程基线 tree 一致的原批准输入 |
| Pilot V2 最终冻结 delta | `c313f59a72b8a69c9ef7c5bdc4ab12c610d76a37` | 本地 code-complete 唯一设计输入 |
| engineering 基线 | `68ee563212a63c5b9570a203ec2fca1ad21b3be4` | 冻结稿 cherry-pick 后的实施基线 |
| Sub2API 源码参考 | `41cec0db059ffb82d0efdcfcf07a24ab51fbfe97` | 只读核对正式 API；本计划不修改或部署 |
| one-person-lab-app | `faeb0d6f9d1fe18ac6ea1433168c5696fd7d7918` | 只读确认 `shells/.gitkeep`；本计划禁止修改 |

调查已完整读取 `AGENTS.md`、`docs/invariants.md`、冻结稿、
`packages/contracts/opl-cloud-launch-freeze-contract.json` 和以下 11 份 `state=current` 机器合同：

- `opl-cloud-billing-ledger-contract.json`
- `opl-cloud-business-object-contract.json`
- `opl-cloud-console-source-truth-contract.json`
- `opl-cloud-deployment-contract.json`
- `opl-cloud-evidence-ledger-contract.json`
- `opl-cloud-fabric-resource-catalog-contract.json`
- `opl-cloud-launch-freeze-contract.json`
- `opl-cloud-management-contract.json`
- `opl-cloud-pricing-contract.json`
- `opl-cloud-product-contract.json`
- `opl-cloud-service-boundary-contract.json`

CodeGraph 与 `rg` 确认的当前真实链路：

1. `POST /api/auth/login` -> `controlPlaneServer.login` ->
   `Service.AuthenticateSub2APIUser` -> `Sub2APIHTTPClient.AuthenticateUser`。后者已经解出
   `access_token`，但当前只返回 `Sub2APIIdentity`；`createSession` 只保存 Session 哈希、用户、
   CSRF 和到期时间。
2. `registerGatewayRoutes` 当前只有钱包、全部 Key 列表、默认 Workspace Key Usage/Stats、
   balance history 和固定 `opl-workspace` reveal。`GatewayUsage`/`GatewayUsageStats` 在服务端自动
   选择保留 Key，不能表达通用 Key。
3. Sub2API revision-pinned 路由注册的用户 Key 真相是 `GET/POST /api/v1/keys` 和
   `GET/PUT/DELETE /api/v1/keys/:id`；创建支持 `Idempotency-Key`。管理侧已有分页用户、
   `admin/usage`、`admin/usage/stats`、批量用户/Key Usage、balance history 和
   `create-and-redeem`。
4. `operatorAccountMappings` 当前对每个本地账号串行调用一次 `Service.Sub2APIUser`，是明确的
   运维 N+1。
5. `workspaceLaunchOperation` 当前从 `compute` 开始，计算和存储分别持有
   `ComputeBillingOperationID`/`StorageBillingOperationID`，链路是
   `compute -> storage -> attachment -> workspace -> receipt`。
6. `ClaimWorkspaceRenewal`/`PersistWorkspaceRenewal` 已提供 PostgreSQL CAS + lease 模式；启动链
   应复用这个经过测试的模式，不增加队列。
7. Fabric/Control Plane 已持有 `WorkspaceRuntime.ServiceName`/`runtimeServiceName`；文件查询无需
   新建 Runtime 注册表。
8. `one-person-lab-app` 不是当前云 Runtime 文件服务源码。Runtime `/projects` API 必须由实际
   active shell/image 仓库的 Runtime owner 单独交付 revision + immutable digest；本仓只接入，
   不修改 `one-person-lab-app`。

### 1.2 当前安全基线

基线已通过 Node 165/165（SKIP 0）、typecheck、lint、build、Fabric `go test ./...`、Ledger
`go test ./...` 和 `sentrux check .`。Control Plane 全量测试只因本机没有 PostgreSQL test
连接而未完成；正式 full gate 必须使用 PostgreSQL 16 和 `OPL_POSTGRES_TESTS=1`，不得把这项
缺口解释为通过。

---

## 2. 冻结 Owner、真相与禁止边界

| 工程面 | Owner 与唯一真相 | 允许的写入 | 禁止 |
| --- | --- | --- | --- |
| Console UI | 展示、交互、来源状态、美元格式和敏感值即时清理 | 只发产品命令 | 计算价格、推断余额/健康、保存秘密、直连任何数据库 |
| Control Plane | Session、映射、权限、命名 DTO、Workspace 商品/操作、公告、运维审计 | 编排并保存命令状态 | 第二份密码、钱包、Key、Usage、文件或供应商事实 |
| Gateway/Sub2API | 唯一钱包、Key、模型路由、逐请求/聚合 Usage | 正式 HTTP API | OPL 直写其 PostgreSQL；OPL 新建镜像库或只读副本 |
| Fabric | 腾讯资源、Kubernetes、挂载、Runtime、Secret 及其读回 | 唯一基础设施写入 | 客户价格、余额、Key 生命周期、账单真相 |
| Runtime | 已挂载 `/projects` 的文件元数据与文件系统用量 | 本期仅只读查询 | 文件正文/上传/下载/删除；把结果写入 Control Plane/Ledger |
| Ledger | 只追加扣款、退款、履约、轮换、公告发布等非秘密证据 | append-only receipt/reference | 拥有余额/API Usage、改资源、存 Key/密码/原始上游响应 |

所有切片继续排除：公开注册、SSO/MFA、在线支付、渠道退款、备份/恢复、HA/SLA、Serve、
完整文件管理器、多 Workspace、升级/降级、公开商业发布。不得增加依赖、服务、队列或表，
除非下文明确列出且现有原语不能承载。

本计划不授权真实收费、真实 Key/模型请求、腾讯购买/续费/删除、部署、push、PR 或 merge。
这些动作即使进入 pilot-ready 验收，也必须由 owner 逐项另行批准。

---

## 3. V2 产品 API 与命名 DTO

以下是 hard cut 后唯一 Console 产品路由。JSON 复用现有 `SourceEnvelope<T>` 和服务端
`writeSourceEnvelope`，不得新造 `ProductSourceEnvelope`。`source`、`status`、`available`、
`fetchedAt` 保持真实；`status` 只允许 `available|empty|unavailable`。`sourceUpdatedAt` 只有权威
来源提供时才透传，禁止用 Control Plane 本地时间伪造；失败响应不能伪造零值或空集合。
Vue 本地 `loading` 不由服务端制造。

| Surface | 路由 | 命名 DTO |
| --- | --- | --- |
| 身份 | `POST /api/auth/login`、`GET /api/auth/me`、`POST /api/auth/logout` | `LoginRequest`、`SessionDTO`、`CurrentAccountDTO` |
| 钱包 | `GET /api/gateway/wallet`、`GET /api/gateway/balance-history` | `GatewayWalletDTO`、`GatewayBalanceHistoryPageDTO` |
| Gateway 地址 | `GET /api/gateway/endpoint` | `GatewayEndpointDTO{baseUrl}`，只读且使用 `SourceEnvelope` |
| Key | `GET/POST /api/gateway/keys`、`GET/PATCH/DELETE /api/gateway/keys/{keyId}` | `GatewayKeyPageDTO`、`GatewayKeySummaryDTO`、`CreateGatewayKeyRequest`、`UpdateGatewayKeyRequest` |
| Key secret | `POST /api/gateway/keys/{keyId}/reveal` | `GatewayKeySecretDTO`，强制 `private, no-store` |
| Key Usage | `GET /api/gateway/keys/{keyId}/usage`、`GET /api/gateway/keys/{keyId}/usage-summary` | `GatewayKeyUsagePageDTO`、`GatewayUsageSummaryDTO` |
| 账号 Usage | `GET /api/gateway/usage-summary` | `GatewayAccountUsageSummaryDTO`，必须来自上游聚合 |
| Workspace | `GET /api/workspaces`、`GET /api/workspaces/{workspaceId}`、`POST /api/workspace-launches`、`GET /api/workspace-launches/{operationId}` | `WorkspaceDTO`、`WorkspaceLaunchRequest`、`WorkspaceLaunchOperationDTO` |
| Workspace Key | `POST /api/workspaces/{workspaceId}/workspace-key/rotate` | `WorkspaceKeyRotationDTO`，只含 ID/fingerprint/status |
| Runtime | `GET /api/workspaces/{workspaceId}/runtime-status`、`GET /api/workspaces/{workspaceId}/files`、`GET /api/workspaces/{workspaceId}/filesystem-usage` | `WorkspaceRuntimeDTO`、`WorkspaceFilePageDTO`、`WorkspaceFilesystemUsageDTO` |
| 用户账单 | `GET /api/billing/receipts`、`GET /api/billing/receipts/{id}` | `BillingReceiptPageDTO`、`WorkspaceBillingReceiptDTO` |
| 公告 | `GET /api/announcements`、`POST /api/announcements/{announcementId}/read` | `AnnouncementPageDTO`、`AnnouncementDTO`、`AnnouncementReadDTO` |
| 运维总览 | `GET /api/operator/overview` | `OperatorOverviewDTO`，每个指标保留独立来源状态 |
| 运维账号 | `GET /api/operator/accounts` | `OperatorAccountPageDTO`、`OperatorAccountDTO` |
| 邀请/禁用账号 | `POST /api/operator/accounts/invitations`、`POST /api/operator/accounts/{accountId}/disable` | `InviteAccountRequest`、`OperatorAccountCommandDTO` |
| 钱包调整 | `POST /api/operator/accounts/{accountId}/wallet-adjustments`、`GET /api/operator/wallet-adjustments/{operationId}` | `WalletAdjustmentRequest`、`WalletAdjustmentOperationDTO` |
| 运维 Workspace | `GET /api/operator/workspaces`、`GET /api/operator/workspaces/{workspaceId}` | `OperatorWorkspacePageDTO`、`OperatorWorkspaceDTO` |
| Runtime 凭据 | `POST /api/workspaces/{workspaceId}/runtime-credentials/reveal`、`POST .../rotate` | `WorkspaceRuntimeCredentialDTO`，强制 `private, no-store` |
| 自动续费 | `POST /api/workspaces/{workspaceId}/auto-renew` | `WorkspaceAutoRenewRequest`、`WorkspaceAutoRenewCommandDTO` |
| 运维复核/健康 | `GET /api/operator/reconciliation`、`POST /api/operator/billing-reviews/{resourceType}/{id}/resolve`、`GET /api/operator/health` | `OperatorReconciliationPageDTO`、`BillingReviewResolutionRequest`、`OperatorHealthDTO` |
| 运维公告 | `GET/POST /api/operator/announcements`、`PUT /api/operator/announcements/{id}`、`POST .../{id}/publish`、`POST .../{id}/withdraw` | `OperatorAnnouncementPageDTO`、`AnnouncementDraftRequest`、`AnnouncementScheduleRequest` |

浏览器永远不能提交或覆盖 `accountId`、`userId`、`sub2apiUserId`、总价、资源 ID、余额前后值或
事实时间。服务端从 Session、映射、价格快照和权威读回填充这些字段。

公共 API 地址只从独立的 `OPL_GATEWAY_PUBLIC_BASE_URL` 进入 `GatewayEndpointDTO`。生产必须校验为
合法绝对 HTTPS URL；缺失或无效时返回 `unavailable`，且不带 `data`。浏览器响应、Console 构建配置
和任何 fallback 都不得读取、暴露或派生内部 `OPL_SUB2API_BASE_URL`，也不得回退到
`gflabtoken.cn`。

Control Plane adapter 只使用 revision-pinned Sub2API 正式 HTTP API；生产启用前还必须逐项通过只读
compatibility probe：

| 能力 | Sub2API 正式 API |
| --- | --- |
| 单用户/分页用户与余额 | `GET /api/v1/admin/users/:id`、`GET /api/v1/admin/users` |
| 管理侧用户 Key 读回 | `GET /api/v1/admin/users/:id/api-keys` |
| 用户 Key 生命周期 | `GET/POST /api/v1/keys`、`GET/PUT/DELETE /api/v1/keys/:id` |
| 逐请求/聚合 Usage | `GET /api/v1/admin/usage`、`GET /api/v1/admin/usage/stats`，固定 `user_id+api_key_id` |
| 批量用户/Key Usage | `POST /api/v1/admin/dashboard/users-usage`、`POST /api/v1/admin/dashboard/api-keys-usage` |
| 余额记录/幂等调整 | `GET /api/v1/admin/users/:id/balance-history`、`POST /api/v1/admin/redeem-codes/create-and-redeem` |

源码注释中的旧 `/api-keys` 名称不是路由真相；以 revision-pinned route registry 的 `/keys` 和
生产 probe 为准。若生产版本的鉴权、分页、批量、金额或 raw Workspace Key 读回不匹配，停止接入，
不得直连数据库或把 Key/token 持久化来绕过。

---

## 4. 写链闭环与稳定操作身份

每个 mutation 都必须走完整闭环；`network timeout` 不是失败证明，缺少权威读回时只能进入
`unknown/manual_review`，不能盲重放。

| 写链 | 鉴权/校验 | 稳定操作 ID | 权威写入与读回 | 收敛、证据和可见结果 |
| --- | --- | --- | --- | --- |
| 登录/退出 | 密码由 Sub2API 验证；本地映射/email/status 一致 | Session 哈希 | Control Plane Session + 进程内委托凭据；随后 `/auth/me` 读回 | 退出/过期同时删两者；用户看到重新登录，不暴露 token |
| 邀请/禁用账号 | operator + CSRF + email/account/status 校验 | `account-invite-*` / `account-disable-*` | Sub2API identity resolve/create + Control Plane 原子 identity graph；本地状态读回 | AuthAttempt/AdminAuditEvent；Session 失效；运维看到映射健康 |
| 通用 Key create/update/delete | owner Session + CSRF + Key 所有权 + 保留名拒绝 | `Idempotency-Key` -> `gateway-key-*` | 用户 bearer 调 Sub2API；GET Key/404 确认 | RuntimeOperation 只存命令状态，审计不含 Key；UI 重新拉权威列表 |
| Workspace Key 收敛/轮换 | owner Session + Workspace 所有权 + exactly-one invariant | `workspace-key-*` | Sub2API 用户 API 写；Sub2API 读回；Fabric Secret 写与 fingerprint 读回 | Workspace 只存 Key ID；Ledger/审计存 ID/fingerprint；UI 显示真实状态 |
| 运维钱包调整 | operator + CSRF + IP policy + 二次确认 + 映射/金额/关联操作 | `wallet-adjustment-*`，同时派生固定 redeem code | Sub2API `create-and-redeem`；balance history + balance 精确读回 | RuntimeOperation phase、AdminAuditEvent、Ledger reference；运维看到 before/after/source |
| Workspace launch | owner + CSRF + 套餐/容量/余额/主 Workspace/保留 Key 校验 | `workspace-launch-*`，charge/refund/资源/Receipt 分域派生 | Sub2API 一次总扣款；Fabric 创建/读回；Runtime readiness；Ledger 一张购买 Receipt | PostgreSQL CAS phase 收敛；用户看到进度、失败、URL、资源和 Receipt |
| Workspace renewal | 已有 durable intent + 门禁 + 同一 Workspace 价格快照 | `workspace-renewal-*` | Sub2API 一次总扣款；Fabric 原资源续费读回；Ledger 一张续费 Receipt | paidThrough/状态 CAS 收敛；证据不足时开关不可启用 |
| Runtime credential rotate | owner + CSRF + active Workspace | `runtime-credential-*` | Fabric/Runtime Secret 写与 readiness/credential version 读回 | Control Plane 引用 + Ledger/审计；用户只在 no-store 响应看到密码 |
| 自动续费开关 | owner + CSRF + Basic/Pro production evidence gate | Workspace intent CAS | Control Plane Workspace intent 写入并读回；不直接调用腾讯自动续费 | AdminAuditEvent；UI 在 gate 通过前保持 disabled |
| 人工复核 resolution | operator + CSRF + pending operation/evidenceRef 校验 | resolution `Idempotency-Key` | 各权威 owner 精确读回；只执行已冻结的确定性补偿命令 | operation CAS + Ledger/审计；异常清单重新读取 |
| 公告草稿/发布/撤下/已读 | operator 或当前用户 + CSRF + 时间/状态转换校验 | `announcement-*` / `announcement-read-*` | Control Plane PostgreSQL 事务写并读回 | AdminAuditEvent；发布/撤下可追加 Ledger evidence；用户只见当前有效公告 |

Key 原文、Runtime 密码、用户密码、bearer、管理员 token、腾讯密钥和原始上游响应不进入
RuntimeOperation、PostgreSQL 业务表、日志、审计、Ledger、URL、浏览器存储或普通 DTO。

---

## 5. 数据迁移总策略

只新增前向 migration，绝不删除、改写或重排已执行 migration：

1. `202607190001_workspace_api_key_id.sql`：给 `control_plane_workspaces` 增加 nullable positive
   `workspace_api_key_id BIGINT`；旧 active Workspace 必须在 cutover 前由受控收敛命令填充。
   非 active/历史行允许为空；应用层禁止 active Workspace 缺失该 ID。
2. `202607190002_pilot_announcements.sql`：新增 `control_plane_announcements` 与
   `control_plane_announcement_reads`，后者唯一 `(announcement_id,user_id)`；不建通知队列、受众表
   或模板表。

Session 委托凭据、Key、Usage、钱包、Runtime 文件 DTO 均不得新增持久化表。Key/钱包写命令复用
现有 `control_plane_runtime_operations` 保存非权威命令状态，复用
`control_plane_admin_audit_events` 保存非秘密审计。Workspace launch/renewal 继续复用
RuntimeOperation + CAS，不增加队列。

部署切换时旧 Session 无法重建进程内委托凭据，因此必须先统计再清空
`control_plane_sessions`，让 2-5 名 Pilot 用户重新登录；这不是密码或业务数据迁移。

---

## 6. 纵向实施切片

冻结范围覆盖关系：

| 冻结工程范围 | 实施切片 |
| --- | --- |
| current 人类/机器合同 hard cut | S1 定义唯一 V2 contract/DTO，S13 在调用方迁移后删除旧真相 |
| Session 级用户委托凭据 | S2 |
| 通用 Key CRUD/reveal/按 Key Usage | S3 |
| Workspace 自动收敛保留 Key | S4 |
| Sub2API 分页/批量/聚合与 N+1 清退 | S5 |
| 运维充值、扣款、业务退款和审计 | S6 |
| Workspace 一笔总额扣款 | S7 |
| 计算/存储纯履约、补偿和一张 Receipt | S8 |
| Runtime 文件元数据和真实挂载用量 | S9（含独立跨仓 Runtime 前置） |
| 最小公告 | S10 |
| 用户/运维完整 UI/UX | S11、S12 |
| 旧路由/DTO/字段/fallback/测试/冲突文档清退 | S13 |
| PostgreSQL、浏览器、结构、全量和真实环境门禁 | S14 |

下列命令均从仓库根目录开始。涉及 migrations、Ent store 或 CAS 的 RED/PASS 必须连接专用
PostgreSQL 16；在该 slice shell 先设置：

```bash
export PGHOST=127.0.0.1
export PGPORT=5432
export PGUSER=postgres
export PGDATABASE=postgres
export PGSSLMODE=disable
export OPL_POSTGRES_TESTS=1
```

数据库不可用就是未验证，不允许用 skip 或 memory store PASS 代替。

### Slice 1：冻结合同 delta、产品 DTO 和 hard-cut 词汇

**业务结果：** 当前人类/机器合同对 V2 的 owner、真相、目标路由、DTO、美元语义和证据层级只有
一套定义。能力字段区分 `required`、`codeComplete`、`pilotReady`、`productionProven`，
不能因目标写入合同就提前声称可用。

**用户/运维表现：** 本切片不开放按钮；未实现能力仍在 UI 隐藏或显示不可用。用户文案只使用
OPL Cloud、Workspace、API 服务、余额、资源和公告，不出现实现词。

**Owner / 真相 / 写入方：** 架构合同由 repo owner 管理；各事实 owner 按第 2 节不变。合同是
边界真相，不成为运行时余额、资源或 Usage 真相。

**API / 命名 DTO：** 固定第 3 节全部路由和 DTO；在 Go/TS 边界明确
现有 `SourceEnvelope<T>`、`GatewayEndpointDTO`、`MoneyDTO` 和 `OperationStatusDTO`，并冻结
`writeSourceEnvelope` 的真实来源语义。不引入 DTO 生成器或新依赖。

**修改文件：**

- Modify: `README.md`
- Modify: `DEV_GUIDE.md`
- Modify: `docs/invariants.md`
- Modify: `docs/architecture.md`
- Modify: `docs/status.md`
- Modify: `docs/project.md`
- Modify: `docs/decisions.md`
- Modify: `docs/product/console-workspace-v1.md`
- Modify: `docs/runtime/production-runbook.md`
- Modify: 上述 11 份 `packages/contracts/*-contract.json`
- Modify: `packages/contracts/README.md`
- Modify: `apps/console-ui/src/api/dtos.ts`
- Add: `tests/contracts/pilot-v2-hard-cut.test.ts`
- Modify: `tests/contracts/contract-lifecycle.test.ts`
- Modify: 受字段变化影响的现有 `tests/contracts/*.test.ts`

**数据迁移：** 无数据库 migration；每份 current JSON 合同递增 `schemaVersion`。历史计划/设计保留，
但在文首标记 `historical/superseded`，current truth 检查不再引用它们。

**先失败的测试（实际运行并记录 RED）：**

```bash
node --test --test-name-pattern='Pilot V2' tests/contracts/*.test.ts
```

预期 RED：current 合同仍禁止 Key create/revoke、默认 Usage 绑定 `opl-workspace`、启动仍表达两笔
子资源扣款，且没有独立公共 Gateway endpoint、Runtime 文件、钱包调整和公告 owner。

**最小实现：** 只改合同、说明和命名 DTO；复用 `SourceEnvelope<T>`，不新增 envelope；
`deliveryEvidence` 初始仍为 `codeComplete=false`，
不增加路由实现。每个后续切片只在匹配代码+测试存在后更新对应 evidence。

**focused PASS：**

```bash
node --test tests/contracts/pilot-v2-hard-cut.test.ts tests/contracts/contract-lifecycle.test.ts
```

**slice full PASS：**

```bash
npm test
npm run typecheck
```

**原子 commit：** `feat(contracts): hard cut Pilot V2 product DTOs`

**依赖/并行：** 无前置。它是 S2-S12 的命名基线；S9 的外部 Runtime 合同准备可并行，但不得在
本仓写功能。最终删除旧实现和更新 `codeComplete` 证据由 S13 完成。

---

### Slice 2：Session 级 Sub2API 用户委托凭据边界

**业务结果：** Key 用户写操作使用当前用户身份，不使用管理员身份模拟客户；凭据与本地 Session
同生共死，Control Plane 重启或 token 失效后要求重新登录。

**用户/运维表现：** 正常登录响应不变；重启、Session/token 过期或 vault 丢失时返回
`401 reauthentication_required` 并清 Cookie。用户重新登录即可恢复；运维不能查看 token。

**Owner / 真相 / 写入方：** Sub2API 验证密码并签发 bearer；Control Plane 只在单副本进程内
持有短期委托凭据。浏览器、PostgreSQL、Ledger 和日志都不是凭据写入方。

**写闭环：** `/auth/login` 鉴权 -> email/remote ID/status 校验 -> 随机 Session ID -> PostgreSQL
保存哈希 Session + `SessionCredentialVault` 保存 bearer -> `/auth/me` 读回身份 -> 用户看到登录。
logout、过期、用户 disable 和 vault miss 同时清理两侧；任何一步失败回滚已创建的 Session。

**API / 命名 DTO：** `Sub2APIUserAuthentication{Identity, AccessToken}` 只在 Go 内部存在；
`SessionDelegatedCredential` 只含 bearer 与到期时间。`SessionDTO` 禁止出现 access/refresh token。
Key 客户端方法显式接收当前 Session credential，不能回退 admin credential。

**修改文件：**

- Modify: `services/control-plane/internal/clients/sub2api.go`
- Modify: `services/control-plane/internal/clients/sub2api_identity_test.go`
- Modify: `services/control-plane/internal/controlplane/service.go`
- Modify: `services/control-plane/internal/server/auth.go`
- Modify: `services/control-plane/internal/server/auth_accounts.go`
- Modify: `services/control-plane/internal/server/routes_auth.go`
- Modify: `services/control-plane/internal/server/server.go`
- Add: `services/control-plane/internal/server/session_credential_vault.go`
- Add: `services/control-plane/internal/server/session_credential_vault_test.go`
- Modify: `services/control-plane/internal/server/identity_security_test.go`
- Modify: `services/control-plane/internal/server/identity_hard_cut_test.go`
- Modify: `deploy/tke/opl-cloud.k8s.json`
- Modify: `tests/production/tke-kubernetes-manifest.test.ts`
- Modify: `packages/contracts/opl-cloud-management-contract.json`
- Modify: `packages/contracts/opl-cloud-service-boundary-contract.json`
- Modify: `packages/contracts/opl-cloud-deployment-contract.json`

**数据迁移：** 不增加 Session 列；凭据绝不落库。部署合同继续强制 Control Plane
`replicas: 1` + `strategy.type: Recreate`。未来横向扩容需要另立安全 vault 项目，Pilot 不预建。

**先失败的测试：**

```bash
cd services/control-plane
go test ./internal/clients -run 'TestAuthenticateUserReturnsDelegatedCredential' -count=1
go test ./internal/server -run 'Test(SessionCredential|DelegatedCredential|LogoutClearsCredential|VaultMissRequiresLogin)' -count=1
```

预期 RED：当前 `AuthenticateUser` 丢弃 `access_token`，Session 生命周期没有 vault。

**最小实现：** 使用带 mutex 的进程内 map，以 `sessionLookupKey` 为唯一索引；不解析 bearer 取得
授权事实，最多读取 token expiry 作为更短的本地清理时间。测试注入 clock；不增加缓存依赖。

**focused PASS：**

```bash
cd services/control-plane
go test ./internal/clients ./internal/server -run 'Test(AuthenticateUserReturnsDelegatedCredential|SessionCredential|DelegatedCredential|LogoutClearsCredential|VaultMissRequiresLogin)' -count=1
```

**slice full PASS：**

```bash
cd services/control-plane && go test ./internal/clients ./internal/server ./internal/controlplane -count=1
cd ../.. && node --test tests/production/tke-kubernetes-manifest.test.ts
```

**原子 commit：** `feat(auth): bind gateway credentials to console sessions`

**依赖/并行：** 依赖 S1 DTO/合同。S5、S10 的纯读/公告后端可逻辑并行；S3/S4 必须在本切片后。

---

### Slice 3：通用 Key CRUD、secret reveal 与按 Key Usage

**业务结果：** 没有 Workspace 的用户也能创建多个通用 Key、启停、删除、显式查看，并按任意
Key 查看真实 Usage；账号汇总不再通过当前页相加。浏览器只获得独立配置的 OPL 公共 API 地址。

**用户/运维表现：** API 服务页显示 OPL Gateway Base URL、Key 名称/类型/状态/额度/已用/最近
使用；secret 默认遮罩。每个 Key 独立展示 loading/empty/unavailable，失败不清空钱包或其他 Key。

**Owner / 真相 / 写入方：** Sub2API 是 Key/Usage 唯一真相和写入方；Control Plane 只验证当前
Session 映射、所有权、请求形状和命令状态；Console 不持久化原文。

**写闭环：** owner + CSRF -> 名称/额度/有效天数/Key 所有权校验 -> `Idempotency-Key` 派生 operation ->
用户 bearer 调 `/api/v1/keys...` -> GET by ID/404 权威读回 -> RuntimeOperation phase + 非秘密审计 ->
重新拉 Key page -> 用户看到结果。Create 将同一幂等键传上游；update/delete 在网络未知时只凭
精确 GET/404 收敛，不能二次盲写。

**API / 命名 DTO：** 实现第 3 节 Gateway endpoint、Key、Key secret、Key Usage、账号 Usage 路由。
`GatewayEndpointDTO{baseUrl}` 只从 `OPL_GATEWAY_PUBLIC_BASE_URL` 产生；生产非 HTTPS、缺失或无效
均返回 `SourceEnvelope` unavailable，禁止读取或回退 `OPL_SUB2API_BASE_URL`/`gflabtoken.cn`。
`GatewayKeySummaryDTO.kind` 为 `general|workspace`；`manageable` 和 `deletable` 由服务端给出。
`CreateGatewayKeyRequest` 只允许 `name`、`quotaUsdMicros`、`expiresInDays`；Sub2API 创建只写一次，
返回 DTO 透传上游真实 `expiresAt`，禁止 create+update 模拟精确时间。拒绝 custom raw Key、group、
user/account ID 和 `opl-workspace`/内部轮换前缀。Usage 服务端固定 remote user ID + path Key ID。

**修改文件：**

- Modify: `services/control-plane/internal/clients/sub2api.go`
- Modify: `services/control-plane/internal/clients/sub2api_test.go`
- Modify: `services/control-plane/internal/controlplane/service.go`
- Modify: `services/control-plane/cmd/control-plane/main.go`
- Modify: `services/control-plane/cmd/control-plane/main_test.go`
- Modify: `services/control-plane/internal/server/server.go`
- Modify: `services/control-plane/internal/server/routes_gateway.go`
- Modify: `services/control-plane/internal/server/table_store.go`
- Modify: `services/control-plane/internal/server/console_tenant_isolation_test.go`
- Modify: `services/control-plane/internal/server/source_truth_gateway_test.go`
- Add: `services/control-plane/internal/server/gateway_key_commands_test.go`
- Modify: `apps/console-ui/src/api/console-read-api.ts`（仅 API adapter；页面在 S11）
- Modify: `deploy/tke/opl-cloud.k8s.json`
- Modify: `deploy/tke/opl-cloud-production.env.example`
- Modify: `deploy/tke/opl-cloud-staging.local.env.example`
- Modify: `deploy/production-manifest.example.json`
- Modify: `services/control-plane/ops/production-manifest.ts`
- Modify: `tests/production/tke-kubernetes-manifest.test.ts`
- Modify: `tests/production/production-manifest.test.ts`
- Modify: `packages/contracts/opl-cloud-deployment-contract.json`
- Modify: `packages/contracts/opl-cloud-service-boundary-contract.json`
- Modify: `packages/contracts/opl-cloud-console-source-truth-contract.json`
- Modify: `packages/contracts/opl-cloud-launch-freeze-contract.json`

**数据迁移：** 不建 Key/Usage 表。命令状态复用 RuntimeOperation，`result` 只能保存 request hash、
Key ID、目标状态和权威读回摘要，不能保存 Key 原文。

**先失败的测试：**

```bash
cd services/control-plane
go test ./internal/clients -run 'TestUser(KeyCreateIdempotent|KeyCreateExpiresInDays|KeyUpdate|KeyDelete|KeyUsage)' -count=1
go test ./cmd/control-plane ./internal/server -run 'TestGateway(PublicEndpoint|PublicEndpointRequiresHTTPS|PublicEndpointHasNoInternalFallback|GeneralKey|KeyOwnership|KeySecret|PerKeyUsage|AccountUsageSummary)' -count=1
```

预期 RED：客户端没有用户 bearer Key 写方法，路由只支持列表和固定 Workspace Key reveal，Usage
没有 path Key ID，也没有独立公共 Gateway endpoint 配置/DTO。

**最小实现：** 复用现有 `decimalUSDMicros`/`usdMicrosJSON`、HTTP size/timeout guard、
RuntimeOperation、`SourceEnvelope<T>` 和 `writeSourceEnvelope`；不创建 Gateway service、不引入
repository abstraction。公共地址只做标准库 URL 校验和只读投影。

**focused PASS：**

```bash
cd services/control-plane
go test ./cmd/control-plane ./internal/clients ./internal/server -run 'Test(UserKey|GatewayPublicEndpoint|GatewayGeneralKey|GatewayKeyOwnership|GatewayKeySecret|GatewayPerKeyUsage|GatewayAccountUsageSummary)' -count=1
cd ../..
node --test tests/production/tke-kubernetes-manifest.test.ts tests/production/production-manifest.test.ts
```

**slice full PASS：**

```bash
cd services/control-plane && go test ./internal/clients ./internal/controlplane ./internal/server -count=1
cd ../.. && npm run typecheck
```

**原子 commit：** `feat(gateway): manage general keys and per-key usage`

**依赖/并行：** 依赖 S1-S2。与 S5/S6 都会修改 `sub2api.go`，虽业务独立但应串行集成以避免
同一热点文件冲突。旧 Usage/reveal 路由暂不删除，待 S11 迁移调用方后由 S13 404。

---

### Slice 4：Workspace 保留 Key 收敛、轮换与 Secret 一致性

**业务结果：** 每个 Workspace 最终只有一个名为 `opl-workspace` 的保留 Key；启动可幂等创建或
复用，轮换同时更新账号专属 Kubernetes Secret，Workspace 只保存 Key ID。

**用户/运维表现：** 开通前能看到“正在准备 API 凭据”；冲突时停止在可行动的 manual review，
不会先扣款。轮换完成后显示新 Key ID/fingerprint 和更新时间，不显示原文或 Secret 内容。

**Owner / 真相 / 写入方：** Sub2API 写 Key；Fabric 写/读 Kubernetes Secret；Control Plane 写
`workspaceApiKeyId` 和 operation phase；Ledger/审计只写 ID、fingerprint 和结果引用。

**写闭环：** owner + CSRF + Workspace/Key 所有权 -> stable operation -> 用户 API 收敛 Key ->
Sub2API by-ID/list 读回 -> Fabric Secret 写并 fingerprint 读回 -> Workspace ID CAS 更新 ->
Ledger/审计 -> 用户/运维读回。零个创建、一个 active 复用；重复、错误 owner、inactive ambiguity
一律 fail closed。

launch 只把 Key ID 写入 operation/Workspace，不保存 raw value。若进程在 Secret phase 前重启，
worker 只能用已验证映射 + Key ID 通过 Sub2API 管理只读接口重新取得原文并立即写 Fabric Secret；
Key 的创建/改名/删除仍必须使用用户 bearer。生产只读 probe 若不能证明该 readback，启动停在
`manual_review/reauthentication_required`，不得把 raw Key 或 Session bearer 落库。

轮换使用 durable phases：创建 operation-owned 临时 replacement -> Secret 切到 replacement 并读回
fingerprint -> 旧 Key 改为 operation-owned retired 名 -> replacement 改为 `opl-workspace` -> 保存新 ID ->
确认 Runtime 使用新 Secret -> 删除旧 Key。每阶段先读后写；任何未知进入 manual review。临时名称
永不作为通用 Key 展示，且完成时必须收敛为 exactly one reserved Key。

**API / 命名 DTO：** 实现 `POST /api/workspaces/{workspaceId}/workspace-key/rotate` 和
`WorkspaceKeyRotationDTO`。`WorkspaceDTO`/`WorkspaceLaunchOperationDTO` 只返回 Key ID、状态和
fingerprint，不返回 raw Key。S3 通用接口禁止删除 active Workspace 的保留 Key。

**修改文件：**

- Modify: `services/control-plane/internal/clients/sub2api.go`
- Modify: `services/control-plane/internal/clients/sub2api_test.go`
- Modify: `services/control-plane/internal/controlplane/service.go`
- Modify: `services/control-plane/internal/server/routes_workspace_launch.go`
- Modify: `services/control-plane/internal/server/workspace_launch.go`
- Modify: `services/control-plane/internal/server/routes_workspace.go`
- Modify: `services/control-plane/internal/server/workspace_gateway.go`
- Modify: `services/control-plane/internal/server/table_store.go`
- Modify: `services/control-plane/internal/server/memory_table_store.go`
- Modify: `services/control-plane/internal/server/ent_state_store.go`
- Modify: `services/control-plane/ent/schema/shared.go`
- Regenerate: `services/control-plane/ent/*`（只包含 schema 对应机械变化）
- Add: `services/control-plane/migrations/202607190001_workspace_api_key_id.sql`
- Modify: `services/control-plane/migrations/migrations.go`
- Modify: `services/control-plane/migrations/migrations_test.go`
- Modify: `services/control-plane/internal/server/source_truth_gateway_test.go`
- Modify: `services/control-plane/internal/server/workspace_launch_test.go`
- Modify: `services/control-plane/internal/server/runtime_owner_isolation_test.go`
- Modify: `services/fabric/internal/http/server_test.go`
- Modify: `services/control-plane/internal/clients/ledger.go`
- Modify: `services/control-plane/internal/clients/ledger_test.go`
- Modify: `services/ledger/internal/ledger/types.go`
- Modify: `services/ledger/internal/ledger/store_test.go`
- Modify: `services/ledger/internal/http/server_test.go`
- Modify: `packages/contracts/opl-cloud-business-object-contract.json`
- Modify: `packages/contracts/opl-cloud-launch-freeze-contract.json`
- Modify: `packages/contracts/opl-cloud-evidence-ledger-contract.json`

**数据迁移：** 执行 `202607190001_workspace_api_key_id.sql`，不回填猜测值、不保存原文。cutover
前对每个 active Workspace 运行受控收敛并写入 ID；无法证明 exactly one 的行阻断部署。

**先失败的测试：**

```bash
cd services/control-plane
go test ./migrations -run 'TestWorkspaceAPIKeyIDMigration' -count=1
go test ./internal/server -run 'TestWorkspaceKey(Convergence|RotationEveryPhaseResponseLoss|RotationEveryPhaseRestart|RotationSameKeyReplay|RotationConcurrent|RotationTemporaryNameConflict|RotationSecretSwitchedBeforeDatabaseCommit|Secret|Ambiguity|NoRawPersistence)' -count=1
```

预期 RED：Workspace schema 无 Key ID，启动要求预存一个 Key，现有 gateway-secret rotate 只重写
同一 Key，不能完成 Key 生命周期轮换。轮换恢复用表驱动方式在现有
`source_truth_gateway_test.go`/`workspace_launch_test.go` 覆盖每个 durable phase：上游响应丢失、进程
重启、同一幂等键重放、并发轮换、临时名称冲突，以及 Secret 已切换但数据库 phase 未提交。

**最小实现：** 复用 RuntimeOperation、现有 Fabric `WriteGatewaySecret` 和 fingerprint，不建 Key 表、
Secret registry 或新的 worker。仅轮换 operation 使用内部临时名称；Ledger receipt
`workspace.gateway_key_rotated.v1` 只含 old/new Key ID、Secret fingerprint 和 operation reference。

**focused PASS：**

```bash
cd services/control-plane
go test ./migrations ./internal/clients ./internal/server -run 'Test(WorkspaceAPIKeyIDMigration|WorkspaceKey.*(Convergence|Rotation|Secret|Ambiguity|NoRawPersistence))' -count=1
```

**slice full PASS：**

```bash
cd services/control-plane && go test ./... -count=1
cd ../fabric && go test ./internal/http ./internal/fabric -count=1
```

**原子 commit：** `feat(workspace): converge and rotate the reserved gateway key`

**依赖/并行：** 依赖 S2-S3。S7 依赖本切片；S9 Runtime read API 可并行。migration 必须先于应用
使用，但回填/约束 gate 只在 S13 cutover 执行。

---

### Slice 5：Sub2API 分页/批量/聚合接入，清除运维 N+1

**业务结果：** 运维总览、账号、Workspace、复核和健康读面用正式分页/批量/聚合接口取得 2-5 个
账号事实，不再为每个账号串行查用户或 Usage，也不建只读副本。

**用户/运维表现：** 运维可分页查看映射健康、余额、Key 数、API 消费、Workspace 状态；每个来源
显示真实更新时间/缓存新鲜度。资源行完整回答 owner account/user、Workspace、类型、套餐/规格、
provider ID、Zone、状态、创建/到期/最近读回时间和 operation/Receipt 引用。一个远端账号异常只标记
对应来源，不把所有指标补零。

**Owner / 真相 / 写入方：** 本地 user/account/workspace 来自 Control Plane；余额/Key/Usage 来自
Sub2API；Workspace 收费来自 Ledger；资源/健康来自 Fabric。Control Plane 只在请求内 join。

**读链：** operator auth -> 校验分页/日期上限 -> 拉本地映射 -> `GET /admin/users` 分页 -> 分批
`POST dashboard/users-usage`/`api-keys-usage` -> 按 remote ID join -> 保留每源状态/更新时间 -> UI。
禁止 Console 直连 Sub2API、禁止后台全量轮询、禁止前端聚合当前页。

**账号写闭环：** operator + CSRF -> canonical email/account/status 校验 -> stable invite/disable ID ->
Sub2API identity resolve/create（invite）-> Control Plane identity graph 原子写或 lifecycle CAS -> 两侧
身份/本地状态读回 -> AuthAttempt/AdminAuditEvent -> disable 时清 Session/vault -> 运维看到真实映射。
不得用 disable 删除钱包/Key/Workspace，也不提供用户 delete。

**API / 命名 DTO：** 实现 `GET /api/operator/overview`、分页
`GET /api/operator/accounts`/`workspaces`/`reconciliation`、Workspace detail 和
`GET /api/operator/health`；把既有 invite/disable 行为迁到
`POST /api/operator/accounts/invitations` 和 `POST /api/operator/accounts/{accountId}/disable`。
命名 `Sub2APIUserPage`、`Sub2APIBatchUserUsage`、
`Sub2APIBatchKeyUsage`、`OperatorOverviewDTO`、`OperatorAccountPageDTO`、
`OperatorWorkspacePageDTO`、`OperatorResourceDTO`、`OperatorReconciliationPageDTO`、
`OperatorHealthDTO`。`OperatorResourceDTO` 中 owner/account/Workspace/package 由 Control Plane 填充；
resource type/spec/provider ID/Zone/provider status/provider created/expiry/readback 只透传 Fabric；operation
引用来自 Control Plane/Fabric，Receipt 引用只透传 Ledger。缺来源返回对应 unavailable envelope，
不复制 Fabric/Ledger 事实到新表，也不以 `fetchedAt` 伪造 `sourceUpdatedAt`。批量上限
从生产探针确认，Control Plane 自己设置更小硬上限并分批；active Runtime probe 使用有界并发和独立
timeout，不用串行 N+1，也不把“无错误记录”当健康。

**修改文件：**

- Modify: `services/control-plane/internal/clients/sub2api.go`
- Modify: `services/control-plane/internal/clients/sub2api_test.go`
- Modify: `services/control-plane/internal/controlplane/service.go`
- Modify: `services/control-plane/internal/server/routes_admin.go`
- Modify: `services/control-plane/internal/server/routes_state.go`
- Modify: `services/control-plane/internal/server/routes_billing.go`
- Modify: `services/control-plane/internal/server/app_state.go`
- Modify: `services/control-plane/internal/server/source_envelope.go`
- Modify: `services/control-plane/internal/server/resource_facts.go`
- Modify: `services/control-plane/internal/server/operational_alerts.go`
- Add: `services/control-plane/internal/server/operator_projection_test.go`
- Add: `services/control-plane/internal/server/operator_health_test.go`
- Modify: `services/control-plane/internal/server/invited_account_test.go`
- Modify: `services/control-plane/internal/server/identity_security_test.go`
- Modify: `services/control-plane/internal/server/console_tenant_isolation_test.go`
- Modify: `packages/contracts/opl-cloud-management-contract.json`
- Modify: `packages/contracts/opl-cloud-console-source-truth-contract.json`
- Modify: `packages/contracts/opl-cloud-service-boundary-contract.json`

**数据迁移：** 无。不得缓存或持久化用户分页、余额、Key/Usage；Sub2API 未来只读副本只能由其
owner 在正式 API 内部切换，并暴露新鲜度。

**先失败的测试：**

```bash
cd services/control-plane
go test ./internal/clients -run 'TestSub2API(AdminUsersPagination|BatchUsersUsage|BatchKeysUsage)' -count=1
go test ./internal/server -run 'TestOperator(ProjectionUsesBatchAPIs|ProjectionPartialFailure|ProjectionHasNoNPlusOne|ResourceOwnerFields|ResourceUnavailableFields|AccountInvite|AccountDisable)' -count=1
```

预期 RED：客户端缺少分页/批量 adapter，`operatorAccountMappings` 每账号调用 `Sub2APIUser`。

**最小实现：** 在现有 HTTP client 增加三个正式 API adapter；测试 fake transport 统计请求次数，
对 N 个账号断言没有 N 次 user/usage 调用。Pilot 不增加 cache/service/repository。

**focused PASS：**

```bash
cd services/control-plane
go test ./internal/clients ./internal/server -run 'Test(Sub2APIAdminUsersPagination|Sub2APIBatch|OperatorProjection)' -count=1
```

**slice full PASS：**

```bash
cd services/control-plane && go test ./internal/clients ./internal/controlplane ./internal/server -count=1
```

**原子 commit：** `feat(operator): batch account operations and projections`

**依赖/并行：** 依赖 S1；逻辑上可与 S2/S10 并行，但与 S3/S6 共享 `sub2api.go`/admin routes，
integration 应串行 cherry-pick。S12 运维 UI 依赖本切片。

---

### Slice 6：运维充值、扣款、业务退款与审计

**业务结果：** operator 能对已确认映射账号执行一次且可追溯的充值、扣款和业务退款；Sub2API
余额/history 仍是唯一资金真相，Control Plane 只保存命令状态。

**用户/运维表现：** 二次确认对话框显示目标账号、美元金额、原因和关联操作；提交后显示
`pending|succeeded|manual_review`、调整前后余额、history reference、执行人和时间。响应未知时
不显示成功，也不重复扣/加。

**Owner / 真相 / 写入方：** Sub2API `create-and-redeem` 写余额，balance history/balance 读回；
Control Plane RuntimeOperation + AdminAuditEvent 写命令/审计；Ledger 追加 reference，不拥有余额。

**写闭环：** operator + CSRF/IP policy -> account/email/remote ID、十进制美元、kind、reason、
refund related operation、累计退款上限和 confirmation 校验 -> stable op/redeem code -> 读 before ->
`create-and-redeem` -> 精确 history 确认 code/user/type/signed amount/used -> 读 after -> phase CAS ->
Ledger + audit -> 运维读回。缺失/重复/不一致进入 manual_review；同 key 不同 request hash 返回 409。

**API / 命名 DTO：** `WalletAdjustmentRequest{kind,amountUsd,reason,relatedOperationId?,confirmationAccountId}`，
`kind` 只允许 `recharge|debit|business_refund`；服务端精确解析十进制字符串并限制正数/最大值。
`WalletAdjustmentOperationDTO` 返回非秘密 authoritative references。浏览器不能提交 remote user ID、
signed amount、before/after 或 redeem code。

**修改文件：**

- Modify: `services/control-plane/internal/clients/sub2api.go`
- Modify: `services/control-plane/internal/clients/sub2api_test.go`
- Modify: `services/control-plane/internal/controlplane/service.go`
- Modify: `services/control-plane/internal/server/routes_admin.go`
- Add: `services/control-plane/internal/server/wallet_adjustment.go`
- Add: `services/control-plane/internal/server/wallet_adjustment_test.go`
- Modify: `services/control-plane/internal/server/table_store.go`
- Modify: `services/control-plane/internal/server/ent_state_store_test.go`
- Modify: `services/control-plane/internal/clients/ledger.go`
- Modify: `services/control-plane/internal/clients/ledger_test.go`
- Modify: `services/ledger/internal/ledger/types.go`
- Modify: `services/ledger/internal/ledger/store_test.go`
- Modify: `services/ledger/internal/ledger/postgres_store_test.go`
- Modify: `services/ledger/internal/http/server_test.go`
- Modify: `packages/contracts/opl-cloud-management-contract.json`
- Modify: `packages/contracts/opl-cloud-billing-ledger-contract.json`
- Modify: `packages/contracts/opl-cloud-evidence-ledger-contract.json`

**数据迁移：** 无新钱包/账单表。复用 RuntimeOperation action `gateway.wallet_adjustment.v1`；历史
Receipt 不改写。新 Ledger type `gateway.wallet_adjustment.v1` 只记录 operation ID、kind、金额、
authoritative history reference、actor 和关联业务操作。

**先失败的测试：**

```bash
cd services/control-plane
go test ./internal/clients -run 'TestSub2APIAdjustment(ExactAmount|Replay|Unknown)' -count=1
go test ./internal/server -run 'TestWalletAdjustment(Auth|Validation|Idempotency|RefundLink|Readback|Audit)' -count=1
cd ../ledger
go test ./internal/ledger ./internal/http -run 'TestWalletAdjustmentReceipt' -count=1
```

预期 RED：只有内部 monthly charge/refund helper，没有 operator 产品命令、command phase 或 receipt schema。

**最小实现：** 复用 `usdMicrosJSON`、`decimalUSDMicros`、`redeemBalance`、
`confirmAdjustmentReplay`、RuntimeOperation 和 audit；不调用 Sub2API balance 直写 API，不建本地余额。

**focused PASS：**

```bash
cd services/control-plane
go test ./internal/clients ./internal/server -run 'Test(Sub2APIAdjustment|WalletAdjustment)' -count=1
cd ../ledger
go test ./internal/ledger ./internal/http -run 'TestWalletAdjustmentReceipt' -count=1
```

**slice full PASS：**

```bash
cd services/control-plane && go test ./internal/clients ./internal/server -count=1
cd ../ledger && go test ./... -count=1
```

**原子 commit：** `feat(operator): add audited wallet adjustments`

**依赖/并行：** 依赖 S1、S5 的 operator account DTO；可在 S7 前完成。与 S3/S5 同改 Sub2API client，
按 S3 -> S5 -> S6 串行集成最省冲突。

---

### Slice 7：Workspace 一次总额扣款与 PostgreSQL CAS

**业务结果：** 一个 Workspace launch 只生成一笔总额扣款；并发 worker、浏览器关闭和 Control
Plane 重启都不会产生第二笔 charge，且未确认扣款前 Fabric 零写入。

**用户/运维表现：** 购买页只显示 Basic `$52.58` 或 Pro `$240.08` 一笔总价和解释性分项；进度
从“确认购买”进入“已扣款/准备资源”。余额不足、未知和 manual review 是不同状态。

**Owner / 真相 / 写入方：** Control Plane 拥有价格快照和 launch state；Sub2API 写/读一笔余额；
Fabric 此切片只做只读 catalog/preflight；Ledger 暂不写最终 Receipt，留给 S8 完成。

**写闭环：** owner + CSRF -> 主 Workspace/套餐/实时 catalog/preflight/余额/保留 Key 校验 -> 从
`Idempotency-Key` 生成 launch/charge ID 与 request hash -> 持久化 `workspace.launch.v2` -> PostgreSQL
CAS claim -> `create-and-redeem` 一次负值 -> history/balance 精确读回 -> operation phase=`debited` ->
用户轮询看到已扣款。浏览器提交的价格/总额全部拒绝或忽略。

**API / 命名 DTO：** `WorkspaceLaunchRequest{packageId,name}`；auto-renew 在真实门禁前只能为 false。
`WorkspaceLaunchOperationDTO` phase 固定为：

```text
debit_pending -> debited -> compute_fulfilling -> storage_fulfilling -> attaching
-> secret_writing -> runtime_starting -> activating -> receipt_pending -> succeeded

refund_pending -> refunded
unknown/partial -> manual_review
pre-charge terminal validation failure -> failed
```

产品 DTO 不暴露内部 Sub2API/Fabric 名称。

**修改文件：**

- Modify: `services/control-plane/internal/server/routes_workspace_launch.go`
- Modify: `services/control-plane/internal/server/workspace_launch.go`
- Modify: `services/control-plane/internal/server/workspace_launch_test.go`
- Modify: `services/control-plane/internal/server/table_store.go`
- Modify: `services/control-plane/internal/server/memory_table_store.go`
- Modify: `services/control-plane/internal/server/ent_state_store.go`
- Modify: `services/control-plane/internal/server/ent_state_store_test.go`
- Modify: `services/control-plane/internal/server/pricing.go`
- Modify: `services/control-plane/internal/server/pricing_monthly_test.go`
- Modify: `services/control-plane/internal/clients/sub2api_test.go`
- Modify: `packages/contracts/opl-cloud-pricing-contract.json`
- Modify: `packages/contracts/opl-cloud-launch-freeze-contract.json`
- Modify: `packages/contracts/opl-cloud-billing-ledger-contract.json`

**数据迁移：** 不改表；RuntimeOperation JSON 使用 `schemaVersion:2` 和 action
`workspace.launch.v2`。旧 `workspace.launch` 非终态不转换，必须在 S13 cutover 清零/人工处理；终态
历史行保留为证据，不由新 worker 解码。

**先失败的测试：**

```bash
cd services/control-plane
go test ./internal/server -run 'TestWorkspaceLaunch(SingleTotalDebit|CAS|ConcurrentWorkers|Restart|NoFabricBeforeDebit|RejectsClientPrice)' -count=1
```

预期 RED：当前 launch 从 compute/storage 两个 monthly purchase 开始，且 launch persistence 没有
PostgreSQL CAS/lease。

**最小实现：** 将 renewal 的 CAS/lease 形状直接复用于 `ClaimWorkspaceLaunch`/
`PersistWorkspaceLaunch`；不抽象通用 saga/queue。总价只取当前服务端
`pilot-usd-2026-07-v1` 快照。

**focused PASS：**

```bash
cd services/control-plane
go test ./internal/server -run 'TestWorkspaceLaunch(SingleTotalDebit|CAS|ConcurrentWorkers|Restart|NoFabricBeforeDebit|RejectsClientPrice)' -count=1
```

**slice full PASS：**

```bash
cd services/control-plane && go test ./internal/server ./internal/clients ./migrations -count=1
```

**原子 commit：** `feat(workspace): charge launches once with durable claims`

**依赖/并行：** 依赖 S4。S8 必须紧随；此 commit 在 integration 分支可测试但不得部署，因为资源
履约闭环尚未完成。

---

### Slice 8：计算/存储纯履约、失败补偿与单张 Workspace Receipt

**业务结果：** 已确认的一笔 Workspace 扣款驱动 PREPAID compute、CBS、挂载、Secret、Runtime
和激活；计算/存储不再拥有客户扣款身份。成功只生成一张
`billing.workspace_purchased.v1` 客户 Receipt。

**用户/运维表现：** 用户看到一笔 Workspace 账单和资源履约明细；部分资源成功/未知进入人工复核，
不会自动退款或重复购买。Ledger 短暂失败不关停已激活 Workspace，只显示凭证待补。

**Owner / 真相 / 写入方：** Fabric 唯一写/读腾讯、挂载、Runtime 和 Secret；Control Plane 写 phase、
资源引用和 entitlement；Sub2API 只处理总 charge/refund；Ledger 只追加一张购买/退款 Receipt。

**写闭环：** `debited` CAS -> Fabric compute 创建+读回 -> storage 创建+读回 -> attachment+mount 读回 ->
Workspace Key Secret 写+fingerprint -> Runtime fixed digest + readiness/URL/credential status -> entitlement
激活 -> Ledger purchase Receipt -> operation succeeded -> 用户/运维读回。资源创建沿用各自稳定 ID。

补偿固定为：charge 失败时零 Fabric 写；charge 成功且 Fabric 权威确认无计费资源时一次幂等 refund；
任一资源存在或结果未知时 manual review、不自动退款；Receipt 失败只重试 Receipt。renewal 同样只扣
Workspace 总价、续费原 CVM/CBS，再延长 paidThrough 和写一张 renewal Receipt。

**API / 命名 DTO：** 完成 S7 `WorkspaceLaunchOperationDTO`；`WorkspaceBillingReceiptDTO` 包含
`totalUsdMicros`、priceVersion、period、charge reference 和 compute/storage fulfillment references，
components 仅解释分项。Resource DTO 禁止 `billingOperationId`/客户 charge owner 字段。既有
`WorkspaceRuntimeCredentialDTO` reveal/rotate 链必须通过 Secret/readiness/no-store 回归测试；
`WorkspaceAutoRenewCommandDTO` 在 Basic/Pro production renewal evidence 缺失时固定 fail closed。

**修改文件：**

- Modify: `services/control-plane/internal/server/workspace_launch.go`
- Modify: `services/control-plane/internal/server/workspace_launch_test.go`
- Modify: `services/control-plane/internal/server/monthly_billing.go`
- Modify: `services/control-plane/internal/server/monthly_billing_test.go`
- Modify: `services/control-plane/internal/server/workspace_renewal.go`
- Modify: `services/control-plane/internal/server/workspace_renewal_test.go`
- Modify: `services/control-plane/internal/server/renewal_worker.go`
- Modify: `services/control-plane/internal/server/routes_workspace.go`
- Modify: `services/control-plane/internal/controlplane/service.go`
- Modify: `services/control-plane/internal/clients/fabric.go`
- Modify: `services/control-plane/internal/clients/fabric_test.go`
- Modify: `services/control-plane/internal/clients/ledger.go`
- Modify: `services/control-plane/internal/clients/ledger_test.go`
- Modify: `services/control-plane/internal/server/routes_billing.go`
- Modify: `services/control-plane/internal/server/source_truth_ledger_test.go`
- Modify: `services/ledger/internal/ledger/types.go`
- Modify: `services/ledger/internal/ledger/store_test.go`
- Modify: `services/ledger/internal/ledger/postgres_store_test.go`
- Modify: `services/ledger/internal/http/server_test.go`
- Modify: `packages/contracts/opl-cloud-billing-ledger-contract.json`
- Modify: `packages/contracts/opl-cloud-evidence-ledger-contract.json`
- Modify: `packages/contracts/opl-cloud-business-object-contract.json`
- Modify: `packages/contracts/opl-cloud-launch-freeze-contract.json`

**数据迁移：** 不删除 compute/storage 历史 billing 列或 Receipt。新代码停止为客户 launch/renewal
读写这些字段；Workspace `billing_state_json` 保存唯一商品快照。旧字段物理删除留给未来独立
retention migration，不属于 V2。

**先失败的测试：**

```bash
cd services/control-plane
go test ./internal/server -run 'TestWorkspaceLaunch(FulfillmentOnly|SingleReceipt|RefundWhenNoResources|PartialResourceManualReview|ReceiptRetry)' -count=1
go test ./internal/server -run 'TestWorkspaceRenewal(SingleDebit|OriginalResources|SingleReceipt)' -count=1
cd ../ledger
go test ./internal/ledger ./internal/http -run 'TestWorkspacePurchasedReceipt' -count=1
```

预期 RED：compute/storage purchase 会各自 charge/receipt，launch 最后只写非计费 `workspace.created`。

**最小实现：** launch 直接复用现有 Fabric create/sync、attachment、PrepareWorkspace 和 Ledger client；
删除客户子资源 billing 调用，不建立新 fulfillment service。历史 provider/receipt 行仍可查询。

**focused PASS：**

```bash
cd services/control-plane
go test ./internal/server -run 'TestWorkspace(Launch|Renewal).*(Fulfillment|Single|Refund|ManualReview|Receipt)' -count=1
cd ../ledger
go test ./internal/ledger ./internal/http -run 'TestWorkspacePurchasedReceipt' -count=1
```

**slice full PASS：**

```bash
cd services/control-plane && go test ./... -count=1
cd ../fabric && go test ./... -count=1
cd ../ledger && go test ./... -count=1
```

**原子 commit：** `feat(workspace): fulfill resources behind one purchase`

**依赖/并行：** 依赖 S4、S7；与 S9 Runtime metadata 可并行开发，但两者都改 Fabric client，先在
各自分支完成后由 integration owner 顺序 cherry-pick 并解小冲突。

---

### Slice 9：Runtime `/projects` 文件元数据与真实挂载空间用量

**2026-07-20 状态：暂停，不进入本次发布。** 以下内容仅保留为未来独立跨仓工作记录；
本轮不实现 Runtime API、不接入 Cloud route、不展示 Console 文件/容量。发布持久化仅通过
Runtime Pod 直接写入 `/data`、`/projects` 的 SHA256 标记验证，不生成 metadata/statfs evidence。

**业务结果：** 用户可只读浏览当前目录并看到已挂载文件系统总量/已用/可用；CBS 只继续回答
分配容量和挂载事实，不再被误当作文件来源。

**用户/运维表现：** Workspace 页按目录分页、目录优先，显示相对路径、类型、大小、更新时间和
真实空间用量。Runtime 不可达时只让文件/实际用量变为“暂不可用”，套餐容量、资源和账单不丢失。

**Owner / 真相 / 写入方：** Runtime 是文件元数据/statfs 唯一来源；Fabric 用现有 serviceName
解析当前 Runtime 并代理；Control Plane 做 Workspace owner check；所有层仅流式返回，不落库。

**跨仓前置（独立交付，不修改 one-person-lab-app）：** Runtime owner 必须在实际 active shell/image
仓库提交一个原子 commit，提供 `GET /_opl/v1/projects/entries` 和
`GET /_opl/v1/projects/filesystem-usage`、路径逃逸/符号链接/分页测试、内部 service token 鉴权，
并交付 revision + immutable image digest。`/home/dev/one-person-lab-app/shells/.gitkeep` 不得作为
实现位置；没有该外部证据，本仓 S9 只能保持 blocked，不能伪造 Runtime 数据。

**读链：** Console -> Control Plane Session/Workspace owner -> Fabric existing Runtime/serviceName ->
ClusterIP + service token -> Runtime `/projects` -> DTO validation/size limit -> 原路返回。拒绝绝对路径、
`..`、NUL、符号链接、递归、正文读取和超大 page；cursor 只能由 Runtime 解释。

**API / 命名 DTO：**

- `WorkspaceFileEntryDTO{name,relativePath,kind:file|directory,sizeBytes?,updatedAt}`
- `WorkspaceFilePageDTO{path,items,nextCursor,sourceUpdatedAt}`
- `WorkspaceFilesystemUsageDTO{totalBytes,usedBytes,availableBytes,measuredAt}`
- Fabric internal `RuntimeProjectEntries`/`RuntimeFilesystemUsage`；绝不返回 absolute path/service token

**修改文件（仅本仓集成 commit）：**

- Modify: `services/fabric/internal/fabric/types.go`
- Modify: `services/fabric/internal/fabric/service.go`
- Add: `services/fabric/internal/fabric/runtime_metadata.go`
- Add: `services/fabric/internal/fabric/runtime_metadata_test.go`
- Modify: `services/fabric/internal/http/server.go`
- Modify: `services/fabric/internal/http/server_test.go`
- Modify: `services/control-plane/internal/clients/fabric.go`
- Modify: `services/control-plane/internal/clients/fabric_test.go`
- Modify: `services/control-plane/internal/controlplane/service.go`
- Modify: `services/control-plane/internal/server/routes_workspace.go`
- Add: `services/control-plane/internal/server/runtime_metadata_test.go`
- Modify: `services/control-plane/internal/server/console_tenant_isolation_test.go`
- Modify: `deploy/tke/opl-cloud.k8s.json`（service token Secret/NetworkPolicy，不含值）
- Modify: `tests/production/tke-kubernetes-manifest.test.ts`
- Modify: `packages/contracts/opl-cloud-service-boundary-contract.json`
- Modify: `packages/contracts/opl-cloud-console-source-truth-contract.json`
- Modify: `packages/contracts/opl-cloud-business-object-contract.json`
- Modify: `packages/contracts/opl-cloud-deployment-contract.json`

**数据迁移：** 无。文件名、路径、大小、时间和 statfs 结果禁止进入 Control Plane PostgreSQL、
Ledger、缓存或 operation payload。service token 只存在 Fabric-owned Kubernetes Secret。

**先失败的测试：**

```bash
cd services/fabric
go test ./internal/fabric ./internal/http -run 'TestRuntime(ProjectEntries|FilesystemUsage|PathSecurity|ResponseLimit)' -count=1
cd ../control-plane
go test ./internal/clients ./internal/server -run 'TestWorkspaceRuntime(Metadata|Usage|OwnerIsolation|PartialFailure)' -count=1
```

预期 RED：Fabric/Control Plane 没有 metadata adapter，当前 Runtime 只提供 readiness/access 状态。

**最小实现：** 复用现有 Runtime record/serviceName 和标准 HTTP client；不建注册表、文件索引、
递归 scanner、缓存或文件管理器。fake Runtime 必须真实创建临时目录/符号链接并返回受控 statfs。

**focused PASS：**

```bash
cd services/fabric
go test ./internal/fabric ./internal/http -run 'TestRuntime(ProjectEntries|FilesystemUsage|PathSecurity|ResponseLimit)' -count=1
cd ../control-plane
go test ./internal/clients ./internal/server -run 'TestWorkspaceRuntime(Metadata|Usage|OwnerIsolation|PartialFailure)' -count=1
```

**slice full PASS：**

```bash
cd services/fabric && go test ./... -count=1
cd ../control-plane && go test ./internal/clients ./internal/server -count=1
cd ../.. && node --test tests/production/tke-kubernetes-manifest.test.ts
```

**原子 commit：** Runtime 外部仓 `feat(runtime): expose mounted project metadata`；本仓
`feat(runtime): project mounted storage facts`。两个 commit 各自可审查，本仓只 cherry-pick 本仓 commit。

**依赖/并行：** 外部 Runtime commit/digest 是硬前置；其开发可与 S2-S8 并行。本仓 S9 依赖 S1，
并在 S11 UI 前完成。无真实 Runtime 证据最多 code-complete，不能 pilot-ready。

---

### Slice 10：最小公告状态机

**业务结果：** operator 可创建草稿、编辑、定时发布、撤下；全部 Pilot 用户只读取当前有效公告并
幂等标记已读。不增加定向人群、邮件/短信或模板系统。

**用户/运维表现：** 首页/公告页显示标题、正文、发布时间和已读状态；空数据是真 empty，来源失败
是 unavailable。运维看到 draft/scheduled/published/withdrawn 和审计。

**Owner / 真相 / 写入方：** Control Plane PostgreSQL 是公告/已读唯一真相和写入方；发布/撤下进入
Control Plane 运维审计，不要求 Ledger 复制公告。

**写闭环：** operator/user + CSRF -> body/time/state transition/owner 校验 -> stable operation ->
PostgreSQL transaction -> by-ID/active query 读回 -> AdminAuditEvent -> UI。
同一发布 key 不重复；过期时间必须晚于生效时间；撤下不可被普通 edit 自动重发。

**API / 命名 DTO：** 实现第 3 节公告路由和 `AnnouncementDTO`、`AnnouncementDraftRequest`、
`AnnouncementScheduleRequest`、`AnnouncementReadDTO`。正文使用纯文本，不接受 HTML/Markdown
执行内容或外链预览。

**修改文件：**

- Add: `services/control-plane/ent/schema/announcement.go`
- Add: `services/control-plane/ent/schema/announcement_read.go`
- Modify: `services/control-plane/ent/schema/shared.go`
- Regenerate: `services/control-plane/ent/*`（仅新 entity 机械文件）
- Add: `services/control-plane/migrations/202607190002_pilot_announcements.sql`
- Modify: `services/control-plane/migrations/migrations.go`
- Modify: `services/control-plane/migrations/migrations_test.go`
- Modify: `services/control-plane/internal/server/table_store.go`
- Modify: `services/control-plane/internal/server/memory_table_store.go`
- Modify: `services/control-plane/internal/server/ent_state_store.go`
- Add: `services/control-plane/internal/server/routes_announcements.go`
- Modify: `services/control-plane/internal/server/server.go`
- Add: `services/control-plane/internal/server/announcements_test.go`
- Modify: `services/control-plane/internal/server/ent_state_store_test.go`
- Modify: `packages/contracts/opl-cloud-management-contract.json`
- Modify: `packages/contracts/opl-cloud-business-object-contract.json`

**数据迁移：** 执行 `202607190002_pilot_announcements.sql`；新表初始为空，不从 Sub2API 公告表导入，
避免第二真相/来源混合。历史 migration 不改写。

**先失败的测试：**

```bash
cd services/control-plane
go test ./migrations -run 'TestPilotAnnouncementsMigration' -count=1
go test ./internal/server -run 'TestAnnouncement(Draft|Publish|Withdraw|Schedule|ActiveRead|MarkRead|TenantSecurity|Idempotency)' -count=1
```

预期 RED：当前没有公告 entity/store/routes。

**最小实现：** 两张表、一个 routes 文件、现有 audit helper；不增加 scheduler，active query 按请求
时间判断 starts/ends。定时发布不是后台推送。

**focused PASS：**

```bash
cd services/control-plane
go test ./migrations ./internal/server -run 'Test(PilotAnnouncementsMigration|Announcement)' -count=1
```

**slice full PASS：**

```bash
cd services/control-plane && go test ./migrations ./internal/server -count=1
```

**原子 commit：** `feat(announcements): publish pilot notices`

**依赖/并行：** 依赖 S1；与 S2-S9 业务独立，可并行。S11/S12 UI 依赖其 API。Ent generated files
只由本切片修改，避免与 S4 schema commit 并行 cherry-pick；integration 顺序为 S4 后 S10。

---

### Slice 11：用户侧完整 UI/UX

**业务结果：** 用户在一个 OPL Console 完成首页判断、Workspace 购买/访问、API Key/Usage、账单和
公告；不需要理解内部服务，也不会因一个来源失败失去其他已确认事实。现有公共 Home、Login 和
Logo/品牌入口保持原样，不属于 V2 重设计范围。

**用户表现：** 桌面/移动均有 Home、Workspace、API 服务、账单、公告；每块独立 loading、empty、
unavailable、error/retry。Workspace 进度有界轮询；Key/Runtime secret 显式 reveal/copy，离页、刷新、
超时、logout 立即清空。Workspace 页必须分别回答 URL、用户名、密码 reveal/copy，以及对应
Workspace Key reveal/copy。自动续费在真实 Basic/Pro renewal evidence 前保持 disabled 并显示状态原因。

**Owner / 真相 / 写入方：** UI 只消费命名 DTO、格式化美元和发命令；总价、余额、Usage、资源、文件、
健康全部由各 owner DTO 给出。`$0.00` 只能来自明确 available zero。

**写闭环：** 所有按钮复用 S2-S10 服务端闭环；UI 生成 opaque `Idempotency-Key`、带 CSRF、禁用
重复提交、轮询 operation/readback 后显示结果。前端不乐观改余额/Key/Workspace，不从分项算总价。

**API / 命名 DTO：** 使用 S1 `dtos.ts` 全部用户 DTO。Base URL 只消费 S3
`SourceEnvelope<GatewayEndpointDTO>`；缺配置显示 unavailable，禁止读取/回退
`OPL_SUB2API_BASE_URL`、gflabtoken link/iframe/fallback。Workspace Key 根据
`WorkspaceDTO.workspaceApiKeyId` 复用 S3 `POST /api/gateway/keys/{keyId}/reveal`，不新增秘密存储、
前端 fallback 或第二条 Key API。新增 exact `formatUsd` 复用现有整数 formatter，不用浮点推导金额。

**修改文件：**

- Modify: `apps/console-ui/src/App.vue`
- Modify: `apps/console-ui/src/console-model.ts`
- Modify: `apps/console-ui/src/styles.css`
- Modify: `apps/console-ui/src/api/dtos.ts`
- Modify: `apps/console-ui/src/api/auth-api.ts`
- Modify: `apps/console-ui/src/api/console-read-api.ts`
- Modify: `apps/console-ui/src/api/workspaces-api.ts`
- Modify: `apps/console-ui/src/api/console-api.ts`
- Modify: `tests/ui/vue-console-model.test.ts`
- Modify: `tests/ui/vue-console-surface.test.ts`
- Modify: `tests/ui/gateway-request-lifecycle.test.ts`
- Modify: `tests/ui/balance-availability.test.ts`
- Add: `tests/ui/pilot-v2-customer-flow.test.ts`

**数据迁移：** 无。禁止 localStorage/sessionStorage/IndexedDB 保存 Key、密码或 bearer；只在当前 Vue
组件内存保存 reveal value，并用 timer + route/logout cleanup。

**先失败的测试：**

```bash
node --test --test-name-pattern='Pilot V2 customer|Home Login Logo unchanged|Workspace access answers' tests/ui/*.test.ts
npm run typecheck
```

预期 RED：当前 UI 只有旧骨架、固定 Workspace Key/Usage 路径，没有通用 Key、文件、公告及完整来源
状态；Workspace 页面也没有同时回答 URL、用户名、密码和对应 Key。Home/Login/Logo 回归先锁定
现有 DOM、文案与入口，防止 V2 工作流误改公共表面。

**最小实现：** 沿用现有 Vue app、API modules、styles 和 model；不引入 router/store/design system
依赖，不先做无关组件重构，不重新设计 Home/Login/Logo。页面按真实任务组织，敏感 reveal 使用
现有 modal/panel pattern。

**focused PASS：**

```bash
node --test tests/ui/pilot-v2-customer-flow.test.ts tests/ui/gateway-request-lifecycle.test.ts tests/ui/balance-availability.test.ts tests/ui/vue-console-surface.test.ts
npm run typecheck
```

**slice full PASS：**

```bash
npm test
npm run typecheck
npm run lint
npm run build
```

**原子 commit：** `feat(console): complete Pilot V2 customer workflows`

**依赖/并行：** 依赖 S2-S4、S7-S10。不能与 S12 并行修改 `App.vue`/model/styles；按用户 UI 后运维 UI
串行集成，避免人为拆出无价值组件层。

---

### Slice 12：运维侧完整 UI/UX

**业务结果：** operator 能在 Console 内判断账号/钱包/Workspace/资源/计费/当前健康，执行钱包调整、
处理 manual review 和发布公告，无需查数据库或拼接 N 个请求。

**运维表现：** Overview 指标明确口径；Accounts 分页展示映射/余额/Key/Workspace；每条 Resource
明确展示 owner account/user、Workspace、资源类型、套餐/规格、provider ID、Zone、状态、创建时间、
到期时间、最近读回时间及 operation/Receipt 引用；Billing 展示 balance history、Workspace Receipt、
Fabric op 和 reconciliation 异常；Health 只展示真实 probe/时间；Announcements 完整状态机。
Accounts 提供邀请和禁用，不提供删除。所有危险命令二次确认。

**Owner / 真相 / 写入方：** UI 不拥有任何事实，只消费 S5/S6/S8-S10 operator DTO；不能显示 raw Key、
密码、上游响应、数据库字段或管理员 token。owner/account/Workspace/套餐来自 Control Plane；
resource type/规格/provider ID/Zone/provider 状态/创建与到期时间/最近读回来自 Fabric；operation 引用
来自 Control Plane/Fabric，Receipt 引用来自 Ledger。缺 owner source 时显示 unavailable，不复制事实。

**写闭环：** 钱包调整、复核、公告沿用服务端稳定 operation；dialog 锁定目标账号/金额/原因，提交后
按 operation ID 权威轮询。网络未知显示“结果待确认”，不提供“再扣一次”快捷操作。

**API / 命名 DTO：** 使用 `OperatorOverviewDTO`、`OperatorAccountPageDTO`、
`WalletAdjustmentOperationDTO`、`OperatorWorkspaceDTO`、`OperatorReconciliationPageDTO`、
`OperatorResourceDTO`、`OperatorHealthDTO`、`OperatorAnnouncementPageDTO`。
`OperatorResourceDTO` 对上述必需字段保留 owner-specific `SourceEnvelope`/availability，来源未提供
时不以空字符串或 Control Plane 当前时间补齐。健康 DTO 没有采样时为 unavailable，不根据空错误
列表推断 healthy。

**修改文件：**

- Modify: `apps/console-ui/src/App.vue`
- Modify: `apps/console-ui/src/console-model.ts`
- Modify: `apps/console-ui/src/styles.css`
- Modify: `apps/console-ui/src/api/dtos.ts`
- Modify: `apps/console-ui/src/api/console-read-api.ts`
- Modify: `apps/console-ui/src/api/console-api.ts`
- Modify: `tests/ui/vue-console-model.test.ts`
- Modify: `tests/ui/vue-console-surface.test.ts`
- Add: `tests/ui/pilot-v2-operator-flow.test.ts`
- Modify: `tests/ui/task12-truth.test.ts`

**数据迁移：** 无；表格筛选/分页状态可留内存或 URL 非秘密 query，不缓存响应事实。

**先失败的测试：**

```bash
node --test --test-name-pattern='Pilot V2 operator|Operator resource owner fields' tests/ui/*.test.ts
npm run typecheck
```

预期 RED：当前 operator 页面没有批量总览、钱包 adjustment dialog、完整 owner-scoped 资源字段、
文件/真实用量、完整 health 和公告。

**最小实现：** 在现有 UI 信息架构内补齐 operator tabs/dialog/states；不另建 admin frontend，不用
图表库计算未定义指标。2-5 人 Pilot 优先可扫描表格与明确状态。

**focused PASS：**

```bash
node --test tests/ui/pilot-v2-operator-flow.test.ts tests/ui/task12-truth.test.ts tests/ui/vue-console-surface.test.ts
npm run typecheck
```

**slice full PASS：**

```bash
npm test
npm run typecheck
npm run lint
npm run build
```

**原子 commit：** `feat(console): complete Pilot V2 operator workflows`

**依赖/并行：** 依赖 S5-S10 和 S11 的 UI 基线；与 S13 后端清退可在独立分支准备字面盘点，但最终
删除旧 API 必须等本切片迁移完调用方。

---

### Slice 13：迁移消费者后清退旧路由、DTO、字段消费、fallback、测试和冲突文档

**业务结果：** integration HEAD 只有 V2 产品真相；旧客户端收到 404，没有长期 compatibility layer，
同时历史 migration、账单、Receipt、terminal operation 和 Git 历史保持不变。

**用户/运维表现：** 所有已迁移 Console 流程只发 V2 请求。旧书签/API 不返回 410、redirect、旧 DTO
或 fallback，而是 404；无法迁移的旧非终态业务操作在部署前人工处理，不能由新 worker 猜语义。

**Owner / 真相 / 写入方：** 各 owner 不变。Control Plane 删除冲突路由/消费者和子资源客户 billing
写入；文档/合同描述 current V2。数据库只前向增加，不删除历史事实。

**明确 hard-cut 路由清单（全部 404）：**

- `GET /api/gateway/usage`
- `GET /api/gateway/usage/stats`
- `POST /api/gateway/keys/opl-workspace/reveal`
- `POST /api/workspaces/runtime-status`
- `POST /api/workspaces/{workspaceId}/gateway-secret/rotate`
- `GET /api/operator/summary`
- `POST /api/users`、`POST /api/users/disable`、`POST /api/users/delete`；invite/disable 已迁到
  operator account 路由，Pilot V2 不提供用户删除
- 客户直写/直读子资源路由 `/api/compute-pools`、`/api/compute-allocations...`、
  `/api/storage-volumes...`、`/api/storage-attachments...`
- 旧 `POST /api/workspaces` 商品创建入口；唯一购买入口是 `POST /api/workspace-launches`
- 现有 retired registry 中 `/api/me`、`/api/overview`、`/api/gateway/summary`、
  `/api/billing/summary`、`/api/api-keys...`、payment/order/backup/sync/transfer/content 路由继续
  404，不复活

**明确 DTO/字段/fallback 清单：**

- 删除默认选择 `opl-workspace` 的 `GatewayUsage`/`GatewayUsageStats` 调用链和旧 UI adapter。
- 删除固定 `Sub2APIWorkspaceKey` 作为“账号唯一 Key”的产品 DTO 语义；内部 reserved-key validator
  只能服务 Workspace。
- 当前 customer launch/renewal 停止读取/写入 compute/storage 的 `billingOperationId`、
  `billingStatus`、`chargeUsdMicros`、`priceSnapshot` 作为客户购买真相。
- 删除 launch 对 `pricingVersion`/`totalMonthlyPriceCnyCents` 和旧 phase JSON 的 current fallback；
  terminal 历史 row 只保留、不可改写。
- 删除 UI 中实现名称、`micros` 文案、gflabtoken fallback、客户端合计、缺源补零和 secret 持久化。
- 删除/改写只证明旧语义的 compatibility tests；`routes_retired_test.go` 扩展为完整 404 allowlist。
- current 文档删除“Pilot 禁止 Key create/revoke”“Usage 默认 Workspace Key”“两笔客户扣款”等冲突；
  历史文档标记 superseded，不删除。

**API / 命名 DTO：** 只保留第 3 节 V2 路由/DTO。`retiredConsoleAPI` 返回标准 404，不返回兼容 payload。

**修改文件：**

- Modify: `services/control-plane/internal/server/server.go`
- Modify/Delete current registration from: `routes_gateway.go`、`routes_resources.go`、
  `routes_workspace.go`、`routes_state.go`
- Modify: `services/control-plane/internal/server/routes_retired_test.go`
- Modify: `services/control-plane/internal/server/workspace_launch.go`
- Modify: `services/control-plane/internal/server/monthly_billing.go`
- Modify: `services/control-plane/internal/server/renewal_worker.go`
- Modify: `services/control-plane/internal/controlplane/service.go`
- Modify: `apps/console-ui/src/api/*.ts`
- Modify: `apps/console-ui/src/console-model.ts`
- Modify: 旧语义相关 `tests/ui/*.test.ts`、`tests/contracts/*.test.ts`、Control Plane tests
- Modify: Slice 1 所列 current docs 和 11 份 current JSON contracts
- Modify: `docs/runtime/production-runbook.md`
- Modify headers only: `docs/superpowers/specs/2026-07-16-launch-delivery-v2-design.md`
- Modify headers only: `docs/superpowers/specs/2026-07-16-pilot-b-rolling-four-lanes-design.md`
- Modify headers only: `docs/superpowers/plans/2026-07-16-launch-delivery-v2.md`
- Modify headers only: `docs/superpowers/plans/2026-07-16-slides-1-3-launch-operation.md`
- Modify headers only: `docs/superpowers/plans/2026-07-16-slides-4-8-10-production-proof.md`
- Modify headers only: `docs/superpowers/plans/2026-07-16-slides-5-7-customer-facts.md`
- Modify headers only: `docs/superpowers/plans/2026-07-16-slide-6-runtime-owner-isolation.md`
- Add: `tests/contracts/pilot-v2-retired-truth.test.ts`
- Modify: `tests/production/identity-deployment-cutover.test.ts`

**数据迁移/cutover：** 不 drop 列/表、不删除 Receipt。应用部署前必须运行第 7 节 SQL，确认旧 launch、
resource billing、Key rotation、wallet adjustment 等非终态为 0，active Workspace 均有 Key ID；有结果则
停止部署并人工处理。部署窗口统计后删除旧 Session rows，迫使安全重登。

**先失败的测试：**

```bash
cd services/control-plane
go test ./internal/server -run 'TestPilotV2RetiredRoutesReturn404|TestNoLegacyWorkspaceBillingConsumer' -count=1
cd ../..
node --test tests/contracts/pilot-v2-retired-truth.test.ts
```

预期 RED：旧 Gateway/Runtime/operator/resource route 仍注册，旧字段和 fallback 仍有 current 消费者。

**最小实现：** 先确认 S11/S12 没有旧调用，再删路由注册、adapter 和 fallback；保留一张显式 404
测试表，不写 shim、redirect 或版本协商。

**focused PASS：**

```bash
cd services/control-plane
go test ./internal/server -run 'Test(PilotV2RetiredRoutesReturn404|NoLegacyWorkspaceBillingConsumer|Retired)' -count=1
cd ../..
node --test tests/contracts/pilot-v2-hard-cut.test.ts tests/contracts/pilot-v2-retired-truth.test.ts
```

**slice full PASS：**

```bash
npm test
npm run typecheck
cd services/control-plane && go test ./... -count=1
```

**原子 commit：** `refactor(api): remove pre-V2 product truth`

**依赖/并行：** 依赖 S2-S12 全部完成。可以提前只读盘点，但删除动作不可并行提前落地。S14 只在
本切片 clean PASS 后开始。

---

### Slice 14：最终验证门（PostgreSQL、浏览器、结构检查、全量与真实环境分级）

**业务结果：** 同一 integration HEAD 对数据库并发/迁移、桌面/移动交互、owner 边界、全量测试和
真实环境证据有可重复门禁；任何层级都不能用较低等级证据冒充较高等级结论。本切片只证明
S1-S13，不新增业务功能、产品路由或产品状态。

**用户/运维表现：** 本地 Playwright harness 覆盖用户/运维所有 available/empty/unavailable/error、
重复提交、secret 清理和响应式布局，并在桌面/移动断言现有 Home、Login、Logo/品牌入口未改变；
它拦截 API 且禁止访问真实 Sub2API、腾讯、模型或生产环境。

**Owner / 真相 / 写入方：** 测试 fake 只证明代码行为；PostgreSQL 16 证明持久化/CAS；Sentrux 证明
结构规则；production verifier 只采集相应环境的真实读回证据，不创造事实或自动修复。

**API / 命名 DTO：** 为第 3 节 DTO 增加 contract fixture 校验。新增 browser harness CLI 默认
`--network=fake-only`；production verifier 默认 `--read-only`，任何 Gateway/provider/model write
需要独立 allow flag + owner approval ID，且普通 CI 无法设置。

**修改文件：**

- Add: `tools/pilot-v2-browser-qa.ts`
- Add: `tests/ui/pilot-v2-browser-harness.test.ts`
- Modify: `tools/production-verifier.ts`
- Modify: `tools/production-live-qa.ts`
- Modify: `tools/provider-acceptance.ts`
- Modify: `services/control-plane/ops/production-manifest.ts`
- Modify: `services/fabric/ops/production-readiness.ts`
- Modify: `deploy/production-manifest.example.json`
- Modify: `.github/workflows/pull-request-ci.yml`
- Modify: `tests/production/production-verifier.test.ts`
- Modify: `tests/production/production-live-qa.test.ts`
- Modify: `tests/production/provider-acceptance.test.ts`
- Modify: `tests/production/production-readiness.test.ts`
- Modify: `packages/contracts/opl-cloud-deployment-contract.json`
- Modify: `docs/runtime/production-runbook.md`

**数据迁移：** 无新业务 migration；CI 对 S4/S10 migrations 在空库、顺序升级和已执行重放三种状态
运行。production verifier 只读 schema/version，不执行 DDL。

**先失败的测试：**

```bash
node --test --test-name-pattern='Pilot V2 browser|Home Login Logo unchanged|evidence level' tests/ui/pilot-v2-browser-harness.test.ts tests/production/production-verifier.test.ts tests/production/production-live-qa.test.ts
```

预期 RED：当前没有 V2 fake-only desktop/mobile harness、Home/Login/Logo 浏览器回归，也没有 V2
evidence level/allow-flag 断言。

**最小实现：** 复用已安装 `playwright`、现有 staging UI server、production verifier/live QA 和 manifest；
不引入 Playwright Test runner 或截图服务。harness 对非 localhost/API fixture 请求直接失败。

**focused PASS：**

```bash
node --test tests/ui/pilot-v2-browser-harness.test.ts tests/production/production-verifier.test.ts tests/production/production-live-qa.test.ts tests/production/provider-acceptance.test.ts
npm run build
node tools/pilot-v2-browser-qa.ts --network=fake-only
```

**slice full PASS：** 执行第 8.1 节完整 code-complete gate，一次且只对最终 integration HEAD 执行。

**原子 commit：** `test(release): gate Pilot V2 evidence`

**依赖/并行：** 依赖 S1-S13。浏览器 harness 编写可在 S11/S12 后并行准备，但 full evidence commit
只能基于最终 hard-cut HEAD。真实环境门禁只在 owner 另行批准后执行。

---

## 7. 部署前 Hard-Cut 清单

以下 SQL 只写入 runbook，本计划阶段绝不运行。实际 cutover 必须在目标 PostgreSQL 16 只读执行
前四组查询，保存结果；任何非零结果阻断部署并由 operator 人工处理。表/状态名以 S4/S6-S8 最终
migration contract tests 为准，不允许临场猜字段。

```sql
-- 1. 旧 launch 只有 succeeded/refunded/failed 被视为 terminal。
SELECT id, status, result
FROM control_plane_runtime_operations
WHERE action = 'workspace.launch'
  AND status NOT IN ('succeeded', 'refunded', 'failed');

-- 2. 子资源旧客户 billing operation 必须没有非终态。
SELECT 'compute' AS kind, id, billing_status, billing_operation_id
FROM control_plane_compute_allocations
WHERE billing_status NOT IN ('', 'active', 'refunded', 'failed')
UNION ALL
SELECT 'storage', id, billing_status, billing_operation_id
FROM control_plane_storage_volumes
WHERE billing_status NOT IN ('', 'active', 'refunded', 'failed');

-- 3. V2 命令不得遗留未知写结果。
SELECT id, action, status, result
FROM control_plane_runtime_operations
WHERE action IN ('workspace.launch.v2', 'workspace.key.rotate.v1', 'gateway.wallet_adjustment.v1')
  AND status NOT IN ('succeeded', 'refunded', 'failed');

-- 4. 每个 active customer Workspace 必须已收敛 Key ID。
SELECT id, account_id, state, status
FROM control_plane_workspaces
WHERE customer_product = TRUE
  AND (state IN ('active', 'running') OR status IN ('active', 'running'))
  AND workspace_api_key_id IS NULL;

-- 5. 部署窗口在人工确认 active Session 数后失效旧 Session；不删除用户/密码/业务数据。
SELECT count(*) AS sessions_to_invalidate FROM control_plane_sessions;
DELETE FROM control_plane_sessions;
```

字面 hard-cut 验证命令：

```bash
# 旧路由只能出现在显式 retired test、历史文档或本计划；应用/当前 UI 不得消费。
rg -n '/api/gateway/usage|opl-workspace/reveal|gateway-secret/rotate|/api/operator/summary|/api/workspaces/runtime-status' \
  apps/console-ui services/control-plane tests docs packages/contracts

# current 客户 launch 不得继续拥有子资源 charge 身份。
rg -n 'ComputeBillingOperationID|StorageBillingOperationID|computeBillingOperationId|storageBillingOperationId' \
  services/control-plane apps/console-ui tests packages/contracts

# 用户可见源码不能含实现品牌 fallback；测试 fixture 的禁止词断言可以命中。
rg -n 'gflabtoken|Sub2API|POSTPAID_BY_HOUR' apps/console-ui/src public

# 明确证明没有第二钱包/Key/Usage/文件事实表或直连 Gateway PostgreSQL。
rg -n 'CREATE TABLE|postgres|DATABASE_URL|PGHOST' services/control-plane/migrations apps/console-ui/src \
  packages/contracts/opl-cloud-service-boundary-contract.json

node --test tests/contracts/pilot-v2-hard-cut.test.ts tests/contracts/pilot-v2-retired-truth.test.ts
cd services/control-plane && go test ./internal/server -run 'TestPilotV2RetiredRoutesReturn404' -count=1
```

`rg` 命中不是自动成功/失败：S13 commit 必须在说明中逐条给出 allowlist（retired test、历史文档、
provider prohibition）；任何 current caller/handler/DTO 命中都阻断 commit。

---

## 8. 验收层级与命令

### 8.1 Code-complete

定义：current 人类合同、11 份机器合同、命名 DTO、代码、迁移和 UI 已 hard cut；所有测试只使用
fake/本地 PostgreSQL，不访问或修改真实 Sub2API、模型、腾讯或生产。code-complete 不证明任何
生产接口或资源真实可用。

最终 integration HEAD 运行一次：

```bash
set -euo pipefail

run_go_no_skip() {
  local -a race_args=()
  if [[ "${1:-}" == "--race" ]]; then
    race_args=(-race)
    shift
  fi
  go test -run '^$' ./...
  local package_list
  package_list="$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)"
  local -a test_packages=()
  mapfile -t test_packages < <(sed '/^$/d' <<< "$package_list")
  test "${#test_packages[@]}" -gt 0
  go test "${race_args[@]}" -count=1 -json "${test_packages[@]}" | node -e '
    let output = "";
    process.stdin.setEncoding("utf8");
    process.stdin.on("data", (chunk) => { process.stdout.write(chunk); output += chunk; });
    process.stdin.on("end", () => {
      const events = output.trim().split(/\n/).filter(Boolean).map(JSON.parse);
      const skipped = events.filter((event) => event.Action === "skip");
      if (skipped.length) {
        console.error(`Go SKIP ${skipped.length}`, skipped.map((event) => event.Test || event.Package));
        process.exit(1);
      }
    });
  '
}

git diff --check origin/main...HEAD

node --test --test-reporter=tap "tests/**/*.test.ts" | node -e '
  let output = "";
  process.stdin.setEncoding("utf8");
  process.stdin.on("data", (chunk) => { process.stdout.write(chunk); output += chunk; });
  process.stdin.on("end", () => {
    const matches = [...output.matchAll(/^# skipped (\d+)$/gm)];
    if (matches.length !== 1 || Number(matches[0][1]) !== 0) {
      console.error("Node SKIP result missing or nonzero");
      process.exit(1);
    }
  });
'
npm run typecheck
npm run lint
npm run build

export PGHOST=127.0.0.1 PGPORT=5432 PGUSER=postgres PGDATABASE=postgres PGSSLMODE=disable \
  OPL_POSTGRES_TESTS=1 OPL_CAPACITY_TESTS=1

(
  cd services/internal/postgresmigrate
  run_go_no_skip --race
)
(
  cd services/control-plane
  run_go_no_skip
)
(
  cd services/fabric
  run_go_no_skip
)
(
  cd services/ledger
  run_go_no_skip
)

node tools/pilot-v2-browser-qa.ts --network=fake-only
sentrux check .
git status --short --branch
```

要求：上述命令自动拒绝 Node 非零/缺失 SKIP 汇总和任意 Go JSON `Action=skip`。所有 PostgreSQL
suites 显式设置 `OPL_POSTGRES_TESTS=1`；Control Plane 同时设置 `OPL_CAPACITY_TESTS=1`，因此才允许
声明其全量 Go SKIP 0。若容量环境不能运行，就取消 Control Plane 全局 SKIP 0 和 code-complete 声明，
不得口头降级。PostgreSQL migrations/CAS、desktop 1440x900、mobile 390x844、Home/Login/Logo、
用户/运维两角色、available/empty/unavailable/error/secret cleanup 全部通过；Sentrux
Critical/Important 为 0；worktree clean。

### 8.2 Pilot-ready

定义：在 code-complete commit/digest 不变的前提下，获得 owner 对每种真实写操作的单独批准，并
完成 invite-only 受控 Pilot 环境证据：

1. 生产 Sub2API 正式路由/鉴权/分页/批量上限/金额语义先通过 read-only probe。
2. 受控账号真实登录、通用 Key create/update/delete/reveal、一次真实模型请求、按 Key Usage 和余额
   变化读回全部关联同一 operation/request evidence。
3. Basic/Pro 各一次真实开通或批准的 retained Acceptance：一笔总扣款、CVM/CBS PREPAID、挂载、
   Runtime fixed digest、URL/凭据、文件摘要/statfs 和一张 purchase Receipt。
4. 自动续费开关开放前，Basic/Pro 各一次真实续费、重放、失败恢复和到期证据。
5. immutable image rollout/readiness、桌面/移动真实浏览器、回滚演练通过，且无未处理 manual review。

默认只读命令必须先运行：

```bash
node tools/production-verifier.ts --read-only
node tools/production-live-qa.ts --read-only
node tools/provider-acceptance.ts --read-only
```

任何 mutation command 必须要求 manifest 中独立的 `approvalId`、目标 account/workspace/resource allowlist
和一次性 allow flag；普通 CI、release、E2E 不得具备这些值。本计划不提供或执行真实 mutation 命令。

### 8.3 Production-proven

定义：pilot-ready 的同一 immutable commit/digest 已部署到目标 production namespace，并为每条 V2
写链保存“请求身份 -> 权威写入 -> 权威读回 -> Control Plane 收敛 -> Ledger/审计 -> production
浏览器结果”的证据 bundle；完成一次 Control Plane 重启后的安全重登/operation resume、一次 rollout
和一次可验证 rollback，且 production reconciliation 无未解释 mismatch。

Production-proven 只证明该 revision、该环境、该受控 2-5 人 Pilot 的已执行链路；不等于 saleable、
HA、备份、SLA 或公开注册。没有真实环境证据只能声明 code-complete，禁止越级使用
pilot-ready/production-proven。

---

## 9. 依赖图、并行建议与 integration 顺序

```text
S1 contracts/DTO
 +-> S2 session -> S3 general Key -> S4 Workspace Key -> S7 single debit -> S8 fulfillment
 +-> S5 batch operator -> S6 wallet adjustment -------------------------------+
 +-> S10 announcements -------------------------------------------------------+
 +-> S9 Runtime integration (blocked by separate Runtime repo commit/digest) -+
                                                                                |
                                  S11 customer UI -> S12 operator UI -> S13 hard cut
                                                                                |
                                                              S14 final verification gate
```

推荐并行：

- Runtime owner 的跨仓 S9 前置可与本仓 S2-S8 并行；没有外部 revision/digest 时不伪造实现。
- S10 公告与 S2-S9 业务独立，可单独 worktree 完成；Ent generated commit 在 S4 后 cherry-pick。
- S5 只读运维聚合可与 S2 逻辑并行，但 S3/S5/S6 都碰 `sub2api.go`，integration 顺序固定
  S3 -> S5 -> S6，避免并行改同一热点文件。
- S11/S12 不能并行落 `App.vue`/model/styles；现有单体 UI 下顺序完成比先做组件重构更小、更可审查。
- S13 删除必须等待所有消费者迁移；S14 full gate 只对最终 integration HEAD 运行一次。

每个 slice 的 integration 流程：

```text
clean slice worktree
-> 新增测试并实际运行 RED（记录命令/失败原因，不提交）
-> 最小实现
-> focused PASS
-> slice module/full PASS
-> 一个原子 commit
-> integration owner cherry-pick
-> 检查 git status/diff，不在每个 cherry-pick 后重复 repo 全量
-> S14 最终汇总测试
```

integration commit 顺序：S1、S2、S3、S4、S5、S6、S7、S8、S9、S10、S11、S12、S13、S14。
任何 slice 出现权威 API 与 revision-pinned 源码/生产 read-only probe 不一致、真实结果 unknown、秘密可能
持久化、第二真相或旧非终态无法清零时立即停止，不把半完成 commit cherry-pick 到 integration。

---

## 10. 实施授权与外部门

owner 已批准本计划 delta commit 后立即执行本仓本地 code-complete，不逐 Slice 等待批准。允许最多
3 个无共享写文件的 subagent/独立 worktree，禁止嵌套扩张；integration owner 逐 commit 审查并按
第 9 节顺序集成。S9 只读调查外部 Runtime；没有真实 Runtime commit + immutable digest 时标记
blocked，并继续不依赖 S9 的切片，绝不伪造文件数据。

本授权不包含修改 Sub2API、one-person-lab-app 或任何外部 Runtime 仓库，不包含真实 Gateway/模型写入、
腾讯购买/续费/删除、真实收费、Acceptance、部署、push、PR 或 merge。本地 S14 结束后必须暂停，
等待 owner 对真实环境动作再次批准。
