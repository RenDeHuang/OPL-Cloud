# OPL Cloud Launch Delivery V2 设计

> **历史状态：已取代。** 本文由
> `docs/superpowers/specs/2026-07-16-pilot-b-rolling-four-lanes-design.md`
> 取代，仅保留为 `c46caff` 交付背景；不得继续按本文扩大 Pro、React UI、Key
> 创建或并行 Agent 范围。

## 状态

本设计已由用户确认进入执行。它把十个业务验收阶段编排为六个交付阶段，目标是
在不修改 Sub2API 服务或部署的前提下，交付可真实购买、可进入 Workspace、可使用
Gateway、可回滚的 OPL Cloud。

业务验收仍以十个 `launchStages` 为准；六个阶段只是代码、worktree 和 rollout 的
执行顺序。

## 产品与 Owner

OPL Cloud 有五个产品面：

1. OPL Gateway：Sub2API 提供的模型、Key、余额和请求用量能力。
2. OPL Workspace：运行 `one-person-lab-app` 的云工作台。
3. OPL Console：账号、套餐、购买、Workspace 生命周期和用户操作界面。
4. OPL Fabric：腾讯 CVM/CBS、Kubernetes 绑定、Runtime 和 Secret 注入。
5. OPL Ledger：只追加的交易、资源、验证和恢复证据。

本仓库有四条实现 Owner Lane：Console/Control Plane、Fabric、Gateway 集成和
Ledger。Workspace 是 Fabric 交付的第五个产品面，不是本仓库内第五套服务代码。

## 商品合同

计算与存储是两个独立的月度权益。创建 Workspace 必须同时选择一项有效计算权益
和一项有效存储权益；引导流程根据计算套餐预选对应存储规格，但账单分别列项。

| 套餐 | 计算规格 | 计算月价 | 默认存储 | 存储月价 | 完整 Workspace 月价 |
| --- | --- | ---: | ---: | ---: | ---: |
| Basic | 2 CPU / 4 GB | CNY 350 | 10 GB | CNY 18 | CNY 368 |
| Pro | 8 CPU / 16 GB | CNY 1,500 | 100 GB | CNY 180 | CNY 1,680 |

金额继续使用整数 CNY cents 和 USD micros，按固定 `1 USD = 7 CNY` 一次性向上
换算。Basic 和 Pro 都是目标可售商品，但生产目录只有在对应定价、PREPAID 资源、
幂等购买和真实验收证据通过后才设置为 `available=true`。

## Sub2API 不可变边界

Sub2API 的代码、镜像、容器、数据库、配置和部署均不在本项目修改范围。OPL Cloud
只消费部署版本已有的管理和用户 API：

- `POST /api/v1/admin/users`：创建受控账号；
- `GET /api/v1/admin/users/:id`：读取账号和余额；
- `POST /api/v1/admin/redeem-codes/create-and-redeem`：现有幂等扣款和退款；
- `GET /api/v1/admin/users/:id/api-keys`：读取账号 Key；
- Key DTO 的 `quota_used`、`usage_5h`、`usage_1d`、`usage_7d`：读取真实聚合用量；
- `POST /api/v1/keys`：在该用户认证上下文中创建 Key。

正式实现前必须按用户提供的 `gateway.md` 服务器信息，对生产版本做只读路由和响应
形状核对。核对不得登录主机修改文件、重启服务或执行数据库写入。

`GET /api/v1/admin/users/:id/usage` 在兼容的 `v0.1.155`/`v0.1.156` 中均禁止用作
生产用量证据。管理员也不能代替目标用户创建 Key，因此受控开户和 Key 创建是一次性
Operator Acceptance，不是 Console 请求期间的自动动作；Key 缺失时产品失败关闭。

受控账号使用 `<alias>@fenggaolab.org`，开户密码按用户要求等于 `<alias>`。密码只在
一次开户/登录/Key 创建请求的内存中出现，不进入仓库、日志、OPL 数据库、Ledger 或
测试 fixture。该规则只用于受控 Gateway 身份，不作为 Console 自助注册或公开密码策略。

Console 通过账户映射中的 `sub2apiUserId` 获取余额、唯一名为 `opl-workspace` 的 active
Key 和该 Key 的聚合用量。0 个或多个同名 active Key 都失败关闭，禁止选择列表第一项。
Key 只由经过认证的
账户 Owner 在专用按需接口中读取，默认遮罩，显式操作后才能查看或复制。Key 不进入
`/api/state`、持久化投影、审计正文或浏览器存储。

## 月度资金协议

由于不能为 Sub2API 增加新的冻结接口，月度购买继续使用已经验证的确定性 Redeem Code
和 Idempotency-Key；不得切换到另一条未经验证的余额写路径：

```text
validate account and quote
-> read-only provider capacity and price preflight
-> confirm Sub2API balance
-> idempotent debit
-> provision one-month PREPAID resources
-> claim and read back every provider resource
-> activate entitlement
-> append Ledger receipt
```

资金和失败语义：

- 扣款失败时禁止任何腾讯资源写入。
- 扣款成功但 Fabric 确认未创建任何计费资源时，使用同一操作身份执行一次幂等退款。
- Fabric 返回部分成功或结果未知时，不自动退款，不重复购买，进入 `manual_review`。
- 资源认领成功但 Ledger 失败时，权益保持有效，只重试 Receipt。
- 重放不得产生第二次扣款、退款、购买、续费、Secret 或 Receipt。

现有“先创建资源、再扣款”的路径必须退出，因为 PREPAID 资源一旦创建就已产生云成本。

## Fabric 与 Workspace

客户和验证资源只允许：

```text
InstanceChargeType = PREPAID
Period = 1
RenewFlag = NOTIFY_AND_MANUAL_RENEW
```

`POSTPAID_BY_HOUR` 从生产请求、容量检查、资源认领、测试和文档中删除或改为明确拒绝。

Fabric 直接创建 PREPAID CBS，使用稳定 `ClientToken` 防止重复购买，并把真实 Disk ID
绑定到静态 PV/PVC。存储请求必须携带目标 Compute Zone，PV 使用腾讯 CBS CSI
`com.tencent.cloud.csi.cbs`、`volumeHandle=disk-*`、Zone nodeAffinity、RWO、空
`storageClassName` 和 `persistentVolumeReclaimPolicy=Retain`。资源只有在 CVM ID、CBS ID、
计费类型、到期时间、标签和 Kubernetes 绑定全部读回后才能认领。CBS 在 Workspace Pod
挂载前允许为 `UNATTACHED`，PVC `Bound` 才是部署前存储就绪条件。

计算到期按合同停止或销毁；存储到期保留数据并禁止访问。删除 PVC/PV 只把资源标记为
`retained/released`，不能谎报 CBS 已销毁；只有独立、显式的数据删除命令及腾讯退还结果
读回后才能把 CBS 标记为 destroyed。

Workspace 创建顺序为：

```text
active compute + active storage
-> resolve account-owned opl-workspace Gateway Key
-> Fabric writes/rotates account-scoped Kubernetes Secret
-> deploy pinned one-person-lab-app image
-> mount CBS at /data and /projects
-> wait for /healthz and credentials
-> expose stable Workspace URL
```

Control Plane 通过专用 Secret 写入请求把 Key 瞬时交给 Fabric；Fabric 立即创建或轮换
账户级 Kubernetes Secret，随后 Workspace Runtime 请求只携带 Secret ref。双方都不得
记录 Secret 请求正文，Fabric Operation 只保存 Secret ref、版本和不可逆指纹。
Kubernetes Secret 是唯一授权的 Key 持久化点；OPL 数据库、Ledger、日志、操作 payload、
缓存和浏览器存储均禁止持久化。每个账户使用独立 Key，禁止继续把全局
`OPL_CODEX_API_KEY` 注入所有客户 Workspace。

## App 镜像与回滚

`gaofeng21cn/one-person-lab-app` 是 Workspace 行为 Owner。本仓库不复制 App 源码，
也不从任意 `main` 提交直接发布生产。当前最新非 prerelease 为 `v26.7.13`，公开容器名
是 `ghcr.io/gaofeng21cn/one-person-lab-webui:26.7.13`，已核对与 `stable` 同一 digest：
`sha256:9d867fe0fc9db48b6efa27371d77770e46fc8cd97d26ef85a81fbdac7e96ca76`。
Cloud 验证端口 3000、`/healthz`、密码登录、`/data`、`/projects` 和 Gateway 请求后，
把该源 digest 镜像到 TCR 的 `oplcloud/one-person-lab-app:26.7.13`，再固定目标 digest。

生产部署禁止 `latest`。候选稳定版不可用时，回滚到上一个通过相同黑盒合同的 digest；
App 代码只有在确认镜像合同本身缺失后才在 App 仓库开独立修复 worktree。

## Console 用户体验

现有 Ant Design/ProLayout 和视觉语言保持不变，不做无关重设计。UX worktree 交付完整
交互状态：

- Auth 请求有超时、明确错误和手动重试，不再无限显示统一 Loading。
- Auth、路由懒加载和 Console State 使用可区分的加载状态。
- 主流程是一次“开通 Workspace”向导，依次确认套餐、存储、价格、Key 和购买。
- 计算、存储、挂载继续保留在高级资源页。
- 购买进度显示扣款、PREPAID 开通、资源认领、Secret、Runtime 和 URL 的真实状态。
- Workspace 每 10 秒轮询，最多 5 分钟；终止失败停止轮询，超时后提供手动重试。
- Gateway 页展示 OPL Gateway 品牌、余额、可显式查看/复制的 API Key、Key 状态、
  5h/1d/7d 聚合用量和恢复动作，不展示 mock 月用量或
  内部架构说明。
- Basic/Pro 分别显示计算和存储价格；未通过生产门禁时必须诚实显示不可购买原因。

上线前使用现有生产页面作为视觉基准做桌面和移动浏览器 QA，验证布局、可访问性、错误
状态、敏感信息遮罩和关键操作可达性。

## 唯一验证槽

普通 CI 使用 Fake Sub2API 和 Fake Provider。真实 Runtime 验证复用一个固定包月槽，
不得每次购买或删除资源。

购买前先只读查询生产地域、可用区、TKE 兼容性、PREPAID 库存和报价，选择满足 App
最低运行要求的最便宜 CVM，以及腾讯允许的最小 PREPAID CBS。仅当不存在可复用资源时
允许一次人工 Provider Acceptance 购买。

购买后必须：

- 写入稳定的 CVM/CBS/NodePool/PV 引用和成本标签；
- 将合同的 `perRunTencentPurchase` 固定为 `false`；
- 禁止 CI、release、E2E 和 Agent 再次购买、续费或删除该槽；
- 每次只重建测试 Deployment/Service/Secret 和测试数据；
- 续费必须是未来单独的人工决策，不能自动发生。

## 六个交付阶段

### 1. 合同收口与清退

在 `docs/launch-contract-v2` 中修订合同和测试，吸收旧 P0 的有效 UX 要求，合入主分支。
随后清退已确认过时的 worktree、本地分支和 stash，保留远端分支直到单独确认。主工作区
的 CodeGraph ignore 和 AGENTS 规则纳入正式提交。

### 2. Fabric PREPAID 与 Workspace

在 `fix/fabric-prepaid-workspace` 中交付 CVM/CBS 包月、静态绑定、读回、续费边界、
App digest 和按账户 Secret 注入。

### 3. Gateway 账号投影

在 `feat/gateway-account-projection` 中交付现有 API adapter、账号映射、受控开户、Key/
用量按需读取和 Secret 交接。禁止建立第二个 Gateway 服务或 Key 数据库。

### 4. 商业套餐与月度编排

在 `feat/monthly-commercial-plans` 中交付 Basic/Pro 定价、独立存储权益、扣款优先状态机、
退款/人工复核、续费和 Ledger 证据。

### 5. Console UX

在 `feat/console-launch-ux` 中只消费已合并的产品 API，交付向导、状态、Gateway 和响应式
验收，避免与后端 worktree 同时修改接口定义。

### 6. 验证、合并与 Rollout

在 `test/reusable-launch-verification` 中交付 Fake 商业链、固定包月槽 Runtime E2E、真实
Gateway 请求、不可变镜像验证和回滚证据。通过完整 review/CI 后创建 PR、合并、rollout，
最后执行只读 smoke 和一次固定槽真实 QA。

## 四个 Session

| Session | 责任 | 禁止重叠 |
| --- | --- | --- |
| 主协调 | 合同、Git、集成、review、CI、rollout、成本门禁 | 不直接与 worker 同改功能文件 |
| Fabric worker | 阶段 2 | 不改 Console UI 和 Sub2API |
| Gateway/商业 worker | 阶段 3，随后阶段 4 | 不改 Sub2API 服务和 Fabric provider |
| UX/验证 worker | 阶段 5，随后阶段 6 | 接口合并前不自创后端 shape |

最多三个开发 worktree 同时存在。阶段 4 依赖阶段 2/3 的稳定接口，阶段 5 依赖产品 API，
阶段 6 依赖所有实现合并，因此这些依赖阶段不并行写同一文件。

## 验证与上线门禁

每个功能先写失败测试，再写最小实现。合并前至少通过：

- Node contract/UI/production tests、typecheck、lint 和 build；
- Control Plane、Fabric、Ledger 全量 Go tests 和关键 `-race`；
- PostgreSQL migration 并发/重放测试；
- PREPAID 请求形状、幂等、provider readback 和拒绝 POSTPAID 测试；
- Gateway 租户隔离、Key 不落盘和用量投影测试；
- 桌面/移动浏览器 QA、Workspace 登录和 WebSocket；
- 候选 App digest 的 `/healthz`、持久目录和真实模型请求；
- rollout 后 health/readiness、镜像 digest、数据库恢复和只读生产 smoke。

任何门禁失败都停止 rollout。Cloud 失败回滚到上一 Cloud digest；App 黑盒失败只回滚
Workspace digest；数据库迁移只有在恢复演练和兼容读路径成立时才允许上线。真实资源结果
未知时不得自动删除、退款、续费或再次购买。

## 完成定义

交付完成必须同时满足：合同与实现一致、所有测试通过、PR/CI 完成、生产部署固定 digest、
Basic/Pro 可按真实价格购买、Workspace 使用账户专属 Key、真实 App/Gateway 路径通过、
唯一包月验证槽已固定或已有资源成功复用、后续自动购买被代码和合同共同禁止。
