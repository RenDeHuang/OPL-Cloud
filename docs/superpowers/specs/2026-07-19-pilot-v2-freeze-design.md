# OPL Cloud Pilot V2 产品与工程冻结稿

> 状态：`approved_for_local_code_complete_execution`
> 实现仓库基线：`833599755a05d222683ee81fc74791eb549cda26`
> OPL Cloud 架构基线：`8e1b552bea94ea368ac2f20293b5a601fa3e204e`
> 冻结分支：`codex/pilot-v2-freeze`
> owner 批准输入：`73c0ec09f3eff301cb4df842fda804c26ed37120`
> 日期：2026-07-19

> 2026-07-20 发布 delta：本轮暂停 S9，9.4 节保留为未来目标，不进入发布。
> Console 不提供文件/容量展示，Cloud 不提供对应 API；发布仅通过 Runtime Pod
> 直接写入 `/data`、`/projects` 的 SHA256 标记验证持久化。固定组合为 App
> `6b334ef7f239eb01c40578159e6df9ed2e7f97dc`、Shell
> `dbd9d68115604673df85033d7a0ab323d65a79a2`、Framework
> `51d16f0e93aebf3fd5ccf96082490395fcbb8711`。

## 1. 文档效力

本文冻结 Pilot V2 的目标产品、业务链、用户体验、运维体验、工程归属、数据真相、
清退边界和验收门槛。本文及 owner 批准的最终工程 delta 是 Pilot V2 本地 `code-complete` 的
唯一设计输入。它要求后续同步更新 `docs/invariants.md` 和现有 JSON 机器合同的 `current` 状态，
但本身不证明任何能力已经实现或部署。

批准后的合同更新和实现必须以本文为输入，但不得把本文中的目标状态写成当前状态。
每项能力只有同时具备匹配代码、测试和真实运行证据，才能从 `target` 改为 `available`。

## 2. 冻结结论

1. 一个 Console User 对应一个 OPL Account 和一个 Sub2API User/Wallet。邮箱经
   `lower(trim(email))` 后必须一致；Control Plane 保存映射，不保存第二份密码真相。
2. Sub2API 是唯一余额、API Key、模型路由和逐请求 Usage 真相。用户界面只出现
   OPL Cloud、Workspace 和“API 服务”等产品语言，不出现 Sub2API、gflabtoken、
   PostgreSQL、Fabric、Ledger、CVM、CBS、PV/PVC 等实现词。
3. 用户不购买 Workspace 也可以创建和使用通用 API Key。购买 Workspace 时系统自动
   收敛一个保留的 Workspace Key，并把它交给该 Workspace 使用。
4. 一个账号最多一个主 Workspace。Basic 和 Pro 是两个 Workspace 套餐；每次购买或
   续费只扣一笔 Workspace 总价，计算和存储是该商品的两类履约资源，不是两次客户购买。
5. Console 浏览器只调用 Control Plane 产品 API。Control Plane 调用 Sub2API 正式
   HTTP API，不直连 Sub2API PostgreSQL，不复制钱包、Key、Usage 或余额流水。
6. 运营聚合优先使用 Sub2API 已有分页、批量和聚合 API。需要只读副本时，由 Sub2API
   在自身内部把正式 API 的查询切到只读副本；Console 合同和调用方式不变。
7. Console 的客户与运维金额全部按美元展示，例如 `$52.58`。V2 不增加“把全仓金额
   重写成 USD micros”的项目；现有结算层可继续使用精确机器金额，但 `micros` 字段名和
   单位不得成为界面文案，前端不得用浮点数推导应扣金额。
8. 旧真相采用 hard cut：当前文档、路由、DTO、状态模型、测试和 UI 调用方一次迁移，
   不保留长期兼容接口。已经执行过的数据库迁移和历史账单不可删除或改写，只能停止读取
   旧字段并用前向迁移收敛。
9. Pilot V2 不承诺历史 SLO 或商业 SLA。运维只展示来自真实探针的当前健康、故障和
   最近更新时间；没有来源时显示“暂不可用”，不得推断、补零或制造正常状态。

### 2.1 当前基线事实

`8335997` 已合入 Paid Dual-SKU Pilot Task 1-13 的本地代码，本文 worktree 的基线
Node tests 为 165/165 PASS、SKIP 0，前端 build PASS。但当前仍然只是
`code-complete`，没有生产 Sub2API 鉴权读回、真实腾讯开通/续费、真实模型请求、部署和
浏览器证据，不能称为 `production-proven` 或可售。

| 当前已经存在 | V2 仍需改变或增加 |
| --- | --- |
| 受邀账号与一对一远端计费账户映射 | Key 写操作需要的安全用户会话边界 |
| 余额、Key 列表、Workspace Key reveal、Usage/Stats、余额记录只读投影 | 通用 Key 创建、启停、删除和按任意 Key 查询 |
| Basic/Pro 固定美元报价和一个 Workspace 启动入口 | 启动链从子资源扣款身份 hard cut 为一笔 Workspace 总扣款 |
| PREPAID CVM/CBS、挂载、Runtime、URL 和凭据 readback 代码 | Runtime 文件元数据与挂载空间用量投影 |
| Ledger 客户收费查询和运维 reconciliation | 运维钱包调整、批量总览和公告 |
| Workspace 续费状态机与默认关闭的自动续费 | Basic/Pro 真实续费证据通过后才启用客户控制项 |
| 用户与运维 Console 骨架 | V2 新命令、真实来源状态和桌面/移动完整 UX |

这张表是 revision-pinned 基线，不是永久 truth。实现推进时只能随匹配代码、测试和运行证据
更新，不能因为本文写了目标能力就把右列提前改成完成。

## 3. V2 范围

### 3.1 本期包含

- 2 至 5 个受邀账号；管理员邀请、禁用和查看账号映射。
- 每个账号一个独立钱包映射，运营可审计地充值、扣款和退款。
- 用户可创建多个通用 API Key，并按 Key 查看状态、额度和逐请求 Usage。
- 开通 Workspace 时自动创建或复用一个保留的 Workspace Key。
- Basic 与 Pro 的一次开通、一次总额扣款、计算/存储履约、访问凭据、URL 和账单。
- Workspace 套餐、价格、状态、到期时间、续费状态、计算资源、存储资源和只读文件摘要。
- Workspace 自动续费目标链；在真实续费证据通过前，控制项保持不可启用并说明当前状态。
- 用户余额记录、API 用量、Workspace 收费凭证和来源独立的失败状态。
- 运维总览、账号、钱包调整、Workspace/资源、计费复核、当前系统健康和公告发布。
- 桌面与移动端的完整加载、空数据、不可用、失败、重试、敏感信息遮罩和键盘操作。

### 3.2 本期不包含

- 公开注册、SSO、MFA、忘记密码和完整账号自助生命周期。
- 用户在线支付、支付订单、支付回调、银行卡/微信/支付宝退款、发票和税务。
- 多 Workspace、共享 Workspace、Workspace SSO 和逐 Runtime 请求的 Console 身份绑定。
- 文件正文预览、上传、下载、删除、备份、恢复、同步、跨区复制和数据恢复承诺。
- HA、跨区容灾、大规模自动扩容、历史 SLO 和商业 SLA。
- OPL Serve、公开 Agent 服务、优惠券、升级/降级和公开商业发布能力。
- Console 直连 Sub2API 数据库，或在 OPL 仓库新建第二套 Gateway、钱包、Key 库、
  Usage 库和账单事实库。

运营“退款”在 V2 中仅指对 Sub2API 钱包执行与原业务操作关联的正向余额调整，不等于
支付渠道退款。用户在线支付属于后续独立项目，不能借钱包调整接口伪装实现。

### 3.3 套餐与价格

价格版本固定为 `pilot-usd-2026-07-v1`，币种为 USD，周期为一个月：

| 套餐 | 用户规格 | 计算价格 | 存储价格 | 一次应扣总价 |
| --- | --- | ---: | ---: | ---: |
| Basic | 2 CPU / 4 GB + 10 GB | $50.00 | $2.58 | $52.58 |
| Pro | 8 CPU / 16 GB + 100 GB | $214.28 | $25.80 | $240.08 |

计算与存储价格只是同一 Workspace 商品的解释性分项，不能生成两次客户扣款。Fabric 可根据
地域、可用区和库存选择不同腾讯实例型号，但实际 CPU、内存、存储、PREPAID 周期和人工续费
属性必须满足套餐合同。只有 Fabric 实时目录和生产证据同时通过的套餐才能显示“可开通”。

## 4. 三种视角必须分开

### 4.1 业务链

业务链描述“谁发起、系统产生哪些跨服务副作用、失败后如何恢复、最终真相在哪里”。
例如 Workspace 开通是一条业务链；按钮样式、列表布局不是业务链。

每条业务链必须冻结：发起人、前置条件、唯一业务操作 ID、状态机、写入顺序、权威来源、
补偿条件、人工复核条件和最终凭证。

### 4.2 用户体验

用户体验描述客户要回答的问题和可以执行的命令。页面只消费已冻结的产品 DTO，不认识
Sub2API 管理 DTO、Ent Entity、Fabric Operation 或 Ledger 原始 Receipt。

每个展示块必须声明真实来源，并独立具备 `loading`、`available`、`empty`、
`unavailable` 和可重试失败状态。一个来源失败不能清空其他来源已经确认的事实。

产品 API 复用现有 `SourceEnvelope<T>` 和服务端 `writeSourceEnvelope`，不得另造
`ProductSourceEnvelope`。`source`、`status`、`available`、`fetchedAt` 必须描述本次真实读取；
`sourceUpdatedAt` 只有权威来源明确返回时才能透传，禁止用 Control Plane 本地时间代替来源更新时间。

### 4.3 运维体验

运维体验描述管理员如何邀请账号、确认映射、调整钱包、定位失败、查看资源归属、处理
人工复核、发布公告和判断当前服务是否健康。运维界面可以展示内部 ID 和来源状态，
但仍不能展示原始 API Key、密码、上游响应、数据库结构或供应商密钥。

### 4.4 评审规则

任何需求都必须先归入以上一种主视角，再标出工程 owner。不能用“做一个页面”代替业务
状态机，也不能用“后端已有字段”代替用户体验。一个功能只有同时回答以下问题才可进入实现：

- 谁使用，完成什么结果；
- 哪个系统拥有真相；
- 哪个服务负责写入，哪些服务只读；
- 成功、空数据、不可用和部分失败如何呈现；
- 哪个可运行检查能证明它没有编造数据或重复副作用。

## 5. 工程 Owner 冻结

| 工程面 | 拥有 | 明确不拥有 |
| --- | --- | --- |
| Console UI | OPL 品牌展示、交互状态、美元格式化、敏感信息即时清理、响应式和可访问性 | 钱包、Key、Usage、价格计算、资源状态和文件真相 |
| Console / Control Plane | Session、账号映射、权限、产品 DTO、Workspace 商品与生命周期、业务编排、幂等操作、公告、运维审计 | 第二份密码、钱包、API Key 原文、模型 Usage、腾讯资源、文件正文 |
| Gateway / Sub2API | 用户认证、唯一 USD 钱包、Key 生命周期、模型路由、逐请求 Usage、聚合查询、余额调整 | Workspace 商品、腾讯资源、Workspace 文件、OPL Ledger 凭证 |
| Fabric | 腾讯 CVM/CBS 写入与读回、Kubernetes 绑定、Runtime 和 Secret 注入、资源健康 | 客户价格、余额修改、Key 生命周期、Workspace 续费意图、账单真相 |
| Workspace Runtime | Workspace 登录、工作台、`/projects` 文件元数据和挂载文件系统用量 | 钱包、套餐、腾讯资源所有权、Ledger 凭证 |
| Ledger | 只追加的扣款引用、退款引用、履约、认领、激活、续费、到期、复核和验证证据 | 可花余额、API Usage、原始 Key/密码、资源写入、文件存储 |

Control Plane 是业务编排 owner，不因此成为其他系统数据的 owner。它可以把多个来源组合成
页面 DTO，但不得把组合结果持久化成第二本事实账。

## 6. 身份与账号链

```text
管理员输入邮箱和一次性初始密码
-> Control Plane 规范化邮箱
-> Sub2API 按邮箱解析或创建用户
-> 原子写入 Console User + OPL Account + sub2apiUserId 映射
-> 返回 OPL 计费账户编号和映射状态
-> 用户用同一邮箱、密码登录 Console
-> Control Plane 向 Sub2API 验证凭据并创建自己的安全 Session
```

这不是同步两套账号密码。Sub2API 是密码验证权威；Control Plane 只保存本地 User、Account、
`sub2apiUserId` 和 Session。邮箱或远端 ID 不一致时登录和资金操作都失败关闭。

管理员“用户与计费账户”页必须看到：Console 用户 ID、OPL 账号 ID、规范化邮箱、远端计费
账户 ID、两侧状态和映射健康。客户只看到自己的 OPL 账号和邮箱，不看到远端系统名称或 ID。

Key 写操作复用 Sub2API 已有用户认证 API。Control Plane 登录链需要保留一个仅服务端可用、
与本地 Session 同时过期的短期委托凭据引用；它不得进入浏览器存储、OPL 业务表、日志或
Ledger。凭据失效时要求用户重新登录，不能改用管理员身份模拟客户。

## 7. API Key 与模型用量链

### 7.1 Key 类型

- **通用 Key**：用户不需要 Workspace 即可创建，可用于 Codex、SDK 和直接 API 请求。
- **Workspace Key**：保留名称 `opl-workspace`，一个账号最多一个；开通 Workspace 时由系统
  自动创建或复用，原始值写入该账号的 Kubernetes Secret。

OPL 不增加一张 Key 表。Workspace 只保存 `workspaceApiKeyId`，不保存原始 Key。保留名称
不能被通用 Key 占用；存在零个时可幂等创建，存在一个 active 时复用，存在多个或身份不符时
失败关闭并进入运维处理。

### 7.2 用户命令

用户可以：

- 创建通用 Key，输入名称、美元额度和可选有效天数 `expiresInDays`；
- 查看全部 Key 的名称、状态、额度、已用额度和最近使用时间；
- 显式查看/复制自己的 Key；
- 禁用、启用或删除通用 Key；
- 按 Key 查看逐请求时间、模型、端点、Token、实际美元费用和请求编号。

通用 Key 的数量上限复用 Sub2API 当前配置，不在 OPL 再定义一套数字。活动 Workspace 存在时，
用户不能直接删除保留 Key；轮换必须走 Workspace Key 轮换链，同时更新 Kubernetes Secret。

### 7.3 调用与安全

浏览器调用当前 Session 下的 Control Plane Key API；Control Plane 从路径中的 Key ID 解析
当前用户所有权，再调用 Sub2API 用户 API。浏览器提交的 `user_id`、`sub2apiUserId` 和
`accountId` 一律忽略。

逐 Key Usage 由 Control Plane 服务端固定 `user_id + api_key_id` 后调用 Sub2API。账号汇总
Usage 只能由 Sub2API 正式聚合 API 产生，不能由前端把当前页相加。

原始 Key 默认隐藏，只在 `private, no-store` 的专用响应中返回。页面离开、刷新、超时和退出
登录时立即清空；不得进入 `/api/state`、OPL PostgreSQL、Ledger、日志、缓存和操作 payload。

API 页面展示一个由服务端配置并经过可用性验证的 OPL Gateway 公共 Base URL。不得链接、
iframe 或回退展示 gflabtoken.cn；未配置 OPL 品牌入口时该来源显示暂不可用。

### 7.4 OPL Gateway 公共地址

Control Plane 使用独立的 `OPL_GATEWAY_PUBLIC_BASE_URL`，并通过最小只读产品 API
`GET /api/gateway/endpoint` 返回 `SourceEnvelope<GatewayEndpointDTO>`；
`GatewayEndpointDTO` 只含经校验的公共 `baseUrl`。生产值必须是合法绝对 HTTPS URL，缺失或无效时
返回 `unavailable` 且不带 fallback 数据。浏览器响应、前端配置和日志绝不能暴露、派生或回退到
内部 `OPL_SUB2API_BASE_URL`，也不能出现 `gflabtoken.cn`。

`CreateGatewayKeyRequest` 只接受 `name`、精确美元额度和可选正整数 `expiresInDays`，与锁定的
Sub2API 创建 API 一次写入；返回的 Key DTO 继续展示上游真实 `expiresAt`。禁止通过
create 后再 update 的两次写入模拟精确到期时间。

## 8. 钱包、费用和账单链

### 8.1 三个权威来源

| 用户问题 | 权威来源 | Console 表现 |
| --- | --- | --- |
| 我现在还有多少钱 | Sub2API 用户余额 | OPL 可用余额 |
| 每个 Key 花了多少钱 | Sub2API 逐请求 Usage / 聚合 Usage | API 用量与请求明细 |
| 充值、扣款、退款发生了什么 | Sub2API balance history | 余额记录 |
| Workspace 买了什么、价格和周期 | Control Plane 商品快照 + Ledger Receipt | Workspace 账单 |
| 钱、资源和凭证是否一致 | Control Plane 只读 reconciliation | 仅运维异常列表 |

API Usage 不复制到 Ledger。Ledger 可以记录与 Workspace 履约有关的 Gateway 请求引用，
但不能变成第二份模型调用账单。

### 8.2 美元展示

- 所有客户价格、余额、额度、用量费用、充值、扣款、退款和 Workspace 收费都标明 USD。
- 页面只展示 `$0.00`、`$52.58` 等美元值，不展示 `micros`、内部字段名或 CNY 换算过程。
- `$0.00` 只代表权威来源明确返回零；超时、缺接口、解析失败和身份不一致统一显示“暂不可用”。
- Workspace 应扣金额只接受服务端价格快照，前端不得把计算价、存储价或 Usage 自行相加后提交。
- 腾讯供应商原生成本不是本期 Console 客户账单；它保留在 Fabric 成本证据和运维 runbook，
  不通过临时汇率混入 OPL 美元总账。

### 8.3 运维余额调整

管理员可以对一个已确认映射的账号执行充值、扣款或业务退款。每次操作必须包含：目标账号、
美元金额、原因、关联业务操作（退款时必填）、幂等键、二次确认和执行人。

```text
验证管理员与账号映射
-> 读取当前余额
-> 生成确定性 Redeem Code / Idempotency-Key
-> 调用 Sub2API create-and-redeem
-> 通过响应或精确 balance-history 确认一次结果
-> 追加不含秘密的 Ledger 引用和 Control Plane 运维审计
-> 重新读取余额
```

响应丢失时只能通过相同 code、用户、类型、符号金额和 used 状态确认重放。缺失、重复或不一致
进入 `manual_review`，不得再次调整余额。Control Plane 不写 Sub2API 数据库，也不先改本地余额。

## 9. Workspace 完整业务链

### 9.1 开通

```text
用户选择 Basic 或 Pro
-> Control Plane 读取 Fabric 实时可售目录并生成一笔 Workspace 美元报价
-> 只读检查腾讯容量、价格和账号余额
-> 收敛一个保留 Workspace Key
-> 持久化唯一 workspace.launch 操作
-> 从 Sub2API 一次扣除 Workspace 总月费
-> Fabric 分别创建一个月 PREPAID 计算资源和存储资源
-> Fabric 读回资源 ID、规格、Zone、计费类型、到期时间和归属标签
-> Fabric 绑定存储并写入账号专属 Gateway Secret
-> Fabric 部署固定 digest 的 Workspace Runtime
-> Runtime readiness、URL、用户名和凭据状态读回
-> Control Plane 激活 Workspace 商品权益
-> Ledger 追加一张 Workspace 客户收费凭证及履约引用
```

Key 收敛是非金融前置动作，必须幂等。它失败时不能扣钱或购买腾讯资源。Workspace Key 已创建
但启动尚未持久化时，下一次提交通过保留名称和用户归属复用，不创建第二个 Key。

客户购买的是 Workspace，不分别购买“计算节点”和“存储节点”。Control Plane 可以保存计算、
存储和挂载子操作用于恢复，但只允许一次 Workspace 总额扣款和一张客户收费凭证。

### 9.2 失败恢复

- 扣款失败：禁止任何腾讯资源写入。
- 扣款成功且 Fabric 确认没有产生任何计费资源：执行一次幂等退款。
- 资源部分成功或结果未知：进入 `manual_review`，不自动退款、不重复购买。
- 资源已激活但 Ledger 失败：Workspace 保持可用，只重试 Receipt。
- 浏览器关闭或 Control Plane 重启：从持久化 phase 和稳定操作 ID 继续。
- 任何重放都不能产生第二个 Key、扣款、退款、CVM、CBS、Secret、续费或 Receipt。

### 9.3 用户访问

Workspace URL、用户名和密码是三个独立字段。URL 不能把密码放在 query、fragment 或 userinfo
中。用户在凭据面板显式查看或复制用户名和密码；密码响应为 `private, no-store`，离开页面即清空。
同一页面还必须回答对应 Workspace Key，并提供显式 reveal/copy。Workspace Key 使用 Workspace
保存的 `workspaceApiKeyId` 调用通用 Key 的 `POST /api/gateway/keys/{keyId}/reveal`，复用第 7 节
按 Key 所有权校验、no-store 响应和敏感值清理，不新增秘密存储或第二条 Key reveal API。

Workspace 页面展示：套餐、月价、创建时间、已付至、续费状态、URL、访问状态、计算规格、
存储容量、真实挂载状态和当前 Runtime 健康。供应商 ID 只在运维页出现。

### 9.4 文件与存储摘要

CBS 是块存储，腾讯 API 不能回答“有哪些文件”。文件名、相对路径、类型、大小和更新时间必须
由当前 Workspace Runtime 从已挂载的 `/projects` 提供。

V2 冻结为只读目录浏览：一次只列一个相对目录、服务端分页、目录优先排序，不递归扫描整盘；
不读取正文、不跟随符号链接、不暴露绝对路径、不上传、不下载、不删除。挂载文件系统的总容量、
已用和可用空间也由 Runtime 读取；Fabric 只提供 CBS 分配容量和挂载事实。

调用链为：

```text
Console -> Control Plane owner check -> Fabric 解析当前 Runtime -> Runtime /projects metadata
```

Runtime 不可达时，文件和实际用量显示暂不可用，但已确认的套餐容量、CBS 状态和 Workspace
账单继续显示。文件 DTO 不写入 Control Plane PostgreSQL 或 Ledger。

### 9.5 续费与到期

Workspace 是唯一续费对象，拥有 `autoRenew`、价格快照、周期、`paidThrough` 和续费状态。

```text
续费窗口到达
-> 只读容量和余额检查
-> 一次扣除下一周期 Workspace 总价
-> Fabric 续费原 CVM 与 CBS
-> 读回两个资源的新到期时间
-> 延长 Workspace 权益
-> 追加一张续费 Receipt
```

腾讯自动续费始终关闭。OPL 自动续费只有在 Basic 和 Pro 的真实一次续费、重放、失败恢复和
到期证据通过后才可对客户启用。未付款到期时停止计算和访问，CBS 保留；不得因发布、回滚或
普通验证删除客户数据。

## 10. 用户体验冻结

| 页面 | 必须回答 | 真实来源 | 用户命令 |
| --- | --- | --- | --- |
| 首页 | 余额多少、API 本期花费、Workspace 是否可用、何时到期、是否有需处理失败 | Sub2API、Control Plane、Fabric、Ledger 独立 DTO | 重试单个来源、进入对应页面 |
| Workspace | 可买哪个套餐、总价、开通到哪一步、URL/凭据、计算/存储状态、文件摘要、续费状态 | Control Plane、Fabric、Runtime、Ledger | 开通、查看/轮换凭据、打开 Workspace、切换续费（门禁通过后） |
| API 服务 | Base URL、余额、有哪些 Key、每个 Key 的额度/状态/用量 | Sub2API 正式 API 经 Control Plane | 创建、查看、复制、启停、删除通用 Key，按 Key 查 Usage |
| 账单 | 余额为何变化、Workspace 买了什么、多少钱、哪个周期 | Sub2API balance history、Ledger Receipt | 筛选、分页、查看详情、重试来源 |
| 公告 | 当前有效公告和发布时间 | Control Plane | 标记已读 |

首页不做所有数据的阻塞式大聚合。每个来源独立加载；只有 Workspace 启动进度允许有界轮询。
用户界面不显示架构说明、数据库名、内部 phase、供应商资源名或“来自 Sub2API”等来源文案，
而是转译为“余额暂不可用”“正在准备存储”“等待人工处理”等可行动状态。

现有公共 Home、Login 和 Logo/品牌入口在 V2 保持内容、信息层级和交互不变；S11 不重新设计这些
页面或入口，只允许补充不会改变其既有表现的回归保护。最终验证门必须在桌面和移动浏览器同时
断言三者仍可立即渲染、可操作且视觉标识未被 V2 Console 工作流替换。

## 11. 运维体验冻结

### 11.1 运维总览

总览分别展示并明确统计口径：

- 受邀账号总数、active/disabled 数量：Control Plane；
- 全部客户可用余额合计：Sub2API 分页用户 API；
- 指定周期 API 实际消费、请求数和 Token：Sub2API 聚合 API；
- 指定周期 Workspace 收费总额、退款总额：Ledger 客户收费 Receipt；
- active/provisioning/failed/manual-review Workspace 数量：Control Plane；
- 当前资源异常和服务不可用数量：Fabric 与各服务健康探针。

不得把钱包余额、API 消费、Workspace 收费和腾讯供应商成本相加成一个无定义的“总金额”。

### 11.2 账号与钱包

账号页显示 Console 用户、OPL 账号、计费账户映射、邮箱一致性、状态、当前余额、Key 数量、
Workspace 套餐和状态。运维可邀请、禁用用户，并进入独立的余额调整对话框。

余额调整页支持充值、扣款和业务退款；必须显示调整前后余额、原因、关联操作、执行状态和
审计记录。原始 Key、密码和 Sub2API 管理响应禁止展示。

### 11.3 Workspace 与资源

每个 Workspace 显示 owner、套餐、月价、创建时间、已运行时长、`paidThrough`、续费状态、
URL、Runtime 状态，以及计算/存储/挂载的当前状态。每条运维资源 DTO/UI 必须明确展示 owner
account/user、Workspace、资源类型、套餐/规格、provider ID、Zone、状态、创建时间、到期时间、
最近读回时间及 operation/Receipt 引用。

owner account/user、Workspace 和套餐来自 Control Plane；资源类型、规格、provider ID、Zone、
provider 状态、provider 创建/到期时间和最近读回时间来自 Fabric 权威读回；业务 operation 引用来自
Control Plane/Fabric operation；Receipt 引用来自 Ledger。任一 owner 未提供对应字段时，该字段或
独立来源显示 `unavailable`，不得用空字符串、本地时间或其他 owner 的副本补齐，也不得把 Fabric
或 Ledger 事实复制到 Control Plane 新表。

“谁在什么时候开通了什么”由 Workspace 操作、Fabric provider operation 和 Ledger Receipt
共同回答；不能只看 Control Plane 一行状态推断成功。

### 11.4 计费复核

运维可以按账号、Workspace、状态和时间查询余额记录、Workspace Receipt、Fabric 操作和
reconciliation 异常。系统只生成异常清单，不自动修钱、修资源或补 Receipt。人工处理必须走
确定性命令并留下新的审计与 Ledger 引用。

### 11.5 当前健康

健康页展示 Control Plane、Gateway、Fabric、Ledger 和每个 active Workspace Runtime 的当前
探针状态、最近成功时间、最近错误和可执行重试。它不是 SLA 报表；没有真实采样就显示暂不可用，
不能根据“没有报错记录”推断健康。

### 11.6 公告

公告由 Control Plane 拥有，不借用 Sub2API 公告表。Pilot V2 只支持创建草稿、发布、撤下、
设置生效/失效时间和面向全部 Pilot 用户；客户首页只读取当前有效公告。每次发布或撤下进入运维
审计。定向人群、邮件/短信推送和模板系统不在本期。

## 12. Sub2API 正式 API 与并发策略

### 12.1 首选接口

| 能力 | Sub2API 正式接口 |
| --- | --- |
| 单用户余额 | `GET /api/v1/admin/users/:id` |
| 分页用户与余额 | `GET /api/v1/admin/users` |
| 用户 Key 列表 | `GET /api/v1/admin/users/:id/api-keys` |
| 用户创建/更新/删除 Key | `POST/PUT/DELETE /api/v1/keys...`，使用当前用户认证上下文 |
| 逐请求 Usage | `GET /api/v1/admin/usage?user_id=...&api_key_id=...` |
| Usage 聚合 | `GET /api/v1/admin/usage/stats?user_id=...&api_key_id=...` |
| 批量用户 Usage | `POST /api/v1/admin/dashboard/users-usage` |
| 批量 Key Usage | `POST /api/v1/admin/dashboard/api-keys-usage` |
| 余额记录 | `GET /api/v1/admin/users/:id/balance-history` |
| 幂等余额调整 | `POST /api/v1/admin/redeem-codes/create-and-redeem` |

生产使用前必须用只读探针确认部署版本的路由、鉴权、分页、最大批量、字段和金额语义。仓库中的
Sub2API 源码或 Fake 测试只能证明候选兼容性，不能证明 gflabtoken.cn 当前部署已经具备该能力。

### 12.2 负载规则

- Pilot 的 2 至 5 个账号直接使用正式 API，不提前建设只读副本。
- 用户页面按需加载并分页；API 页面未打开时不查询 Usage，不能后台全量轮询。
- 运维总览使用分页用户 API和批量 Usage API，禁止对 N 个用户串行发 N 次 Usage 请求。
- 批量大小、页大小、日期范围和响应体都有服务端上限；超出时分批，不做无界全量扫描。
- Sub2API 已有的聚合缓存和数据新鲜度字段必须原样纳入来源状态；Console 不伪造更新时间。
- 只有实际监控证明主库读压力成为瓶颈后，Sub2API owner 才为这些正式 API 配置只读副本。
- 只读副本延迟必须通过 `sourceUpdatedAt` 或等价新鲜度暴露；写后确认仍读取主库或强一致接口。
- Console 永远不获取数据库账号，不把 Ent Entity 或 PostgreSQL schema 当成跨服务合同。

## 13. 旧真相 Hard Cut

批准 V2 后，下列当前冲突必须在同一次合同迁移中替换；不能同时保留新旧产品含义：

| 当前冲突 | V2 唯一真相 |
| --- | --- |
| Pilot 禁止 Key create/revoke | 用户可管理通用 Key，Workspace 自动收敛保留 Key |
| 全账号只选唯一 `opl-workspace` Key | 一个保留 Workspace Key + 多个通用 Key；Usage 明确按 Key 查询 |
| Usage/Stats 默认绑定 Workspace Key | 路由 Key ID 经服务端所有权解析；账号聚合走正式聚合 API |
| 运维账号逐用户串行远端读取 | 分页用户接口 + 批量聚合，禁止 N+1 |
| 启动流程保留 compute/storage 两笔客户扣款身份 | 一笔 Workspace 总额扣款；子操作只负责履约恢复 |
| 只展示 CBS 容量，不能回答文件情况 | Runtime 提供只读 `/projects` 元数据和真实挂载用量 |
| 无客户 Key 写入所需会话边界 | 服务端短期委托凭据随 Console Session 生命周期管理 |
| 无运维充值/扣款/退款产品命令 | 确定性余额调整 + balance-history 确认 + 审计与 Ledger 引用 |
| 自动续费只存在隐藏目标 | 仍受真实续费门禁，但目标状态、失败状态和启用条件写入当前合同 |
| 无公告 owner | Control Plane 拥有最小 Pilot 公告 |

Hard cut 的具体含义：

- 更新 `docs/invariants.md`、架构文档、runbook 和所有 current JSON 合同；
- 更新 Control Plane 路由、命名 DTO、状态机和 Console 调用方；旧路由直接 404；
- 删除旧 UI 入口、旧字段消费、旧 fallback、旧兼容测试和当前文档中的冲突说法；
- 已执行数据库迁移只保留为历史，不回写；新增前向迁移，停止读取和写入 retired 字段；
- 历史账单、Receipt 和 Git 历史保持不可变；历史设计只能明确标记 historical/superseded，
  不能被 current truth 检查或实现引用；
- 部署切换前清零或人工处理旧状态机中所有非终态操作，不能让新代码猜测旧操作语义。

## 14. 幂等、故障和秘密

- 邀请账号、创建 Key、Workspace 启动、扣款、退款、资源购买、认领、Secret 写入、续费、
  凭据轮换、Receipt 和运维余额调整各自使用稳定且不同的操作身份。
- 同一幂等键配不同请求哈希必须冲突；同一请求重放返回同一结果或明确 `unknown/manual_review`。
- 写请求的网络超时不是失败证明。没有权威读回时不能自动重试资金或供应商写入。
- Workspace Key 轮换的每个持久化阶段都必须可从响应丢失和进程重启恢复；同键重放、并发轮换、
  临时名称冲突，以及 Secret 已切换但数据库 phase 未提交都必须由现有轮换测试文件覆盖。
- 每个来源独立失败；`empty` 只能来自成功的权威查询，`unavailable` 不携带零值或空集合。
- API Key、Runtime 密码、用户密码、管理员令牌、腾讯密钥和原始上游响应不得进入日志、审计、
  Ledger、浏览器存储或普通 DTO。
- 文件路径必须规范化并限制在 `/projects`；拒绝 `..`、绝对路径、NUL、符号链接逃逸和超大页。
- 运维资金命令需要 operator 权限、CSRF、来源 IP 策略、二次确认和完整非秘密审计。

## 15. 验收与停止条件

### 15.1 Design-complete

- owner 批准本文全部冻结决定；
- 文档不存在待定占位、占位接口、双重真相或未归属能力；
- 白皮书、业务链、用户体验、运维体验和工程 owner 没有边界冲突。

### 15.2 Code-complete

最终验证门只汇总证明 S1-S13 的同一 integration HEAD，不新增业务能力或改变产品范围。

- current 人类合同、机器合同、命名 DTO、代码和 UI 同时完成 hard cut；
- 每个新增业务命令先有失败测试，再有最小实现；
- focused 测试先通过，最终集成 HEAD 只运行一次对应全量测试；
- Node test/typecheck/lint/build、逐 Go module、PostgreSQL、结构检查和桌面/移动浏览器 QA 无 SKIP；
- Node 和 Go 的 SKIP 数必须由命令自动判定。Go 使用 `go test -json` 并在任何 JSON 事件
  `Action=skip` 时失败；Node runner 的 skip 结果也必须由命令解析并失败，不能人工读日志；
- PostgreSQL suites 必须设置 `OPL_POSTGRES_TESTS=1` 并保持 SKIP 0。若声明 Control Plane 全量 Go
  SKIP 0，还必须设置 `OPL_CAPACITY_TESTS=1`；未运行容量测试时必须明确取消全局 SKIP 0 声明；
- 最终 delta 评审的 Critical/Important 为 0。评审不重复从零扩张范围，新增发现只接受能够复现
  本文冻结链路错误、安全问题、数据损失或重复收费的问题。

### 15.3 Pilot-ready

- 生产 Sub2API 正式接口完成只读兼容探针，账号映射、批量接口和 Key 用户上下文真实可用；
- Basic/Pro 对应保留资源或经批准的 Acceptance 已真实读回；
- 至少一个受控账号完成登录、通用 Key 创建、逐请求 Usage 和余额变化；
- Basic 和 Pro 各完成一次真实开通或复用验收，证明一笔总扣款、CVM/CBS、挂载、Runtime、
  URL/凭据、文件摘要和一张 Receipt；
- 续费开关启用前，Basic 和 Pro 各完成一次真实续费及失败恢复证据；
- 发布使用不可变镜像 digest，rollout、readiness、浏览器流程和回滚已证明。

没有这些真实环境证据只能标记 `code-complete`，不能标记 `pilot-ready`、
`production-proven` 或 `saleable`。真实收费、腾讯购买/续费/删除、模型请求、部署、push、PR
和 merge 继续需要用户单独批准。

## 16. 与 OPL Cloud 白皮书的一致性

| 白皮书目标 | V2 选择 | 结论 |
| --- | --- | --- |
| 一个账号一个主 Workspace | 一个账号最多一个可选 Workspace；项目和文件不创建第二个 Workspace | 一致 |
| Gateway 是顶层 AI 能力面 | Sub2API 作为实现后端，用户通过 OPL 品牌 API 服务使用 Key、模型和 Usage | 一致 |
| Console 管账号、额度、账单和资源策略 | Control Plane 编排账号、Workspace、钱包调整和运维体验，不复制其他 owner 真相 | 一致 |
| Fabric 管计算、存储、环境和绑定 | 腾讯与 Kubernetes 写入只在 Fabric | 一致 |
| Ledger 只记证据和引用 | 钱包与 API Usage 留在 Sub2API，文件留在 Runtime/CBS | 一致 |
| Workspace 展示项目、文件、资源和 Receipt | V2 增加只读文件摘要、存储用量、资源状态和收费凭证 | 一致 |
| Gateway Usage 可按账号、Workspace 等归因 | 保留 Workspace Key 提供 Workspace 归因；通用 Key 提供账号/Key 归因 | Pilot 子集 |
| Serve 发布 Agent Service | V2 不实现 Serve，也不把 Workspace URL 当作服务端点 | 明确延期，无冲突 |
| 协作、Package 和 Runway 各有独立 owner | V2 不创建竞争真相 | 一致 |

本文是白皮书目标架构的 invite-only Pilot 子集。白皮书定义未来产品边界，本文定义本实现仓库
下一阶段要落地的精确子集；两者都不能单独证明运行 readiness。
