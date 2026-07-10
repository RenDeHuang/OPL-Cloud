# OPL Gateway 与 Sub2API Console 集成设计

## 状态

方案方向已于 2026-07-11 确认。本文定义分阶段交付设计：使用现有 `https://gflabtoken.cn` Sub2API 部署作为 OPL Gateway 后台，同时保持其服务器更新生命周期独立于 OPL Console。

## 目标

交付统一的 OPL Cloud 客户体验，使用户能够：

1. 通过 OPL 统一身份入口登录。
2. 从 OPL Console 打开 Gateway，无需创建或记忆单独的 Sub2API 密码。
3. 使用 `https://gflabtoken.cn` 现有的 Sub2API 用户门户创建和管理 API Key。
4. 在本地 Codex 和 OPL Workspace 中使用选定的 Key。
5. 在 Console 中查看 Gateway 用量和 OPL 钱包扣款。

Sub2API 继续在当前服务器上独立部署和更新。Console 只通过 HTTP API 与其集成。

## 产品边界

### OPL Console 与 Control Plane 负责

- OPL 账户和组织身份。
- 客户进入 Gateway 的入口。
- OPL 账户与 Sub2API 用户之间的映射。
- Console 中展示的 Gateway 摘要和 Key 投影。
- 为 OPL Workspace 选择 Key。
- OPL 钱包冻结、人民币扣款、收据和对账。
- 针对当前已部署 Sub2API 版本的兼容性检查。

### Sub2API 负责

- API Key 的创建、修改、删除、配额、过期时间和速率限制。
- 模型路由和上游账户选择。
- 请求鉴权和执行。
- 原始请求用量、Token 数量和 `actual_cost` 事实。
- 独立管理其 PostgreSQL 和 Redis 数据。
- 服务器更新和数据库迁移生命周期。

### Fabric 负责

- 将选定的 Gateway Key 写入 Workspace Kubernetes Secret。
- 将 Key 挂载到 `/run/secrets/gateway_api_key`。
- 设置 `OPL_GATEWAY_API_KEY_FILE=/run/secrets/gateway_api_key`。
- 在选定 Key 发生变化时更新 Workspace Secret 和运行时。

## 非目标

- 不在 Console 中复制或重新实现 Sub2API Key 编辑器。
- 不使用 iframe 嵌入 Sub2API UI。
- 不直接读写 Sub2API 数据库。
- 不通过 OPL Control Plane 代理模型流量。
- 不在 Sub2API 中同步或复用 OPL Console 密码。
- 不让 Console 控制 Gateway 服务器安装哪个 Sub2API 版本。
- 不向客户暴露 Sub2API 上游账户、渠道、凭据或路由内部信息。

## 域名与访问模型

`https://gflabtoken.cn` 保持为 Gateway 的规范入口。

面向客户公开的路径：

- 根域名下的 Sub2API 用户门户。
- `/api/v1/auth/*` 下的 OIDC 鉴权端点。
- Sub2API 门户使用的已认证用户 Key 和用量 API。
- `/v1/models`、`/v1/responses` 及兼容供应商路由等模型数据面端点。

受限的管理路径：

- `/api/v1/admin/*` 只允许 OPL Control Plane 和已批准的运维网络访问。
- Control Plane 专用管理员凭据作为部署 Secret 保存，不复用任何人工管理员凭据。

Caddy 边界必须保持模型路由的流式响应能力，并且在不修改 Sub2API 的情况下执行管理路径访问限制。

## 身份设计

一个符合标准的共享 OIDC 签发方作为 OPL Console 和 Sub2API 的凭据权威。稳定身份键为 `(issuer, subject)`，而不是邮箱。

对于现有试点账户，可在已认证的 Console 会话中，或经过管理员批准后，使用已验证邮箱进行一次性 OIDC subject 绑定。完成绑定后，邮箱变化不会改变身份映射。

Control Plane 通过管理员 API 创建 Sub2API 用户，并设置：

- 与 OPL 一致的已验证邮箱和显示名称；
- 角色为 `user`；
- 创建后立即丢弃的随机内部密码；
- 通过 `POST /api/v1/admin/users/:id/auth-identities` 绑定 OIDC 身份。

用户永远不会获得或使用这个随机 Sub2API 密码。用户在 Console 点击 Gateway 后启动或恢复 OIDC 流程，无需第二个产品账户即可进入 Sub2API 门户。

## Control Plane 集成

Control Plane 使用具体的 Sub2API HTTP 客户端，不把 Sub2API 响应直接暴露给浏览器。

所需服务和身份 API：

- `POST /api/v1/auth/login`
- `POST /api/v1/auth/refresh`
- `GET /health`
- `GET /api/v1/admin/system/version`
- `GET /api/v1/admin/users?search=<verified-email>`
- `POST /api/v1/admin/users`
- `GET /api/v1/admin/users/:id`
- `PUT /api/v1/admin/users/:id`
- `POST /api/v1/admin/users/:id/auth-identities`
- `POST /api/v1/admin/users/:id/balance`

所需 Key 和用量 API：

- `GET /api/v1/admin/users/:id/api-keys`
- `GET /api/v1/admin/users/:id/usage`
- `GET /api/v1/admin/usage`
- `GET /api/v1/admin/usage/stats`
- 需要批量摘要时使用 `POST /api/v1/admin/dashboard/api-keys-usage`

当前 Sub2API 管理员 API 可以列出用户的 Key，但不能代用户创建或删除 Key。因此，本设计仍由用户在 Sub2API 用户门户中完成 Key 变更，不需要修改 Sub2API 源码或维护私有 fork。

## Console 用户界面

Gateway 页面使用实时投影替换当前的外部占位页面。

Gateway 概览：

- 在线或不可用状态；
- 规范 Base URL `https://gflabtoken.cn`；
- OPL 钱包可用余额；
- 已分配的 Gateway 预算；
- 今日和本月的请求数、Token 数和费用；
- 最近一次成功用量同步时间。

Key 摘要：

- 名称和掩码后的 Key；
- Sub2API Key ID；
- 正常、禁用、过期或配额耗尽状态；
- 创建时间、最后使用时间和过期时间；
- 配额、已用配额和速率限制窗口；
- Key 被分配到 Workspace 时显示对应 Workspace；
- 通过 SSO 打开 Sub2API Key 门户的操作；
- 为 Workspace 选择现有正常 Key 的操作。

完整 Key 默认隐藏。显示完整 Key 前必须进行近期 Console 身份验证检查；Control Plane 从 Sub2API 获取当前值，以 `Cache-Control: no-store` 返回，并且不将其持久化到 Control Plane 数据库或日志中。

用量与费用：

- 请求时间和请求 ID；
- Key 名称和 ID；
- 通过所选 Key 建立归因时显示关联 Workspace；
- 请求模型和请求类型；
- 输入、输出、缓存读取和缓存写入 Token；
- Sub2API 以美元计价的 `actual_cost`；
- 计价快照和最终 OPL 人民币扣款；
- 按天、模型、Key 和 Workspace 汇总。

## Workspace Key 流程

1. 用户通过 SSO 入口在 Sub2API 门户创建 Key。
2. Console 通过管理员 API 刷新 Key 投影。
3. 用户在创建或更新 Workspace 时选择一个正常 Key。
4. Control Plane 仅在内部资源开通请求中获取完整 Key。
5. Control Plane 通过内部服务连接将敏感值传递给 Fabric。
6. Fabric 将该值写入 Workspace Kubernetes Secret，并且绝不把它写入操作事实、日志、收据或错误载荷。
7. `one-person-lab-app` 通过 `OPL_GATEWAY_API_KEY_FILE` 读取挂载文件，WebUI 不再要求用户输入 Key。

同一个 Key 可以同时用于本地 Codex 和 Workspace。用量归因以 Key 为准：如果某个 Key 已关联 Workspace，同时又在本地使用，则该 Key 的全部用量都会归因到这个 Workspace。需要分开归因时，用户应使用不同的 Key。

如果选定 Key 被禁用、删除、过期或配额耗尽，Console 会将 Workspace Gateway 绑定显示为异常。重新绑定会更新 Secret 并滚动更新 Workspace 运行时。需要模型能力的 Workspace 在没有选择正常 Key 时必须拒绝创建。

## 计费设计

OPL Ledger 是唯一的客户资金权威。Sub2API 余额只是有上限的技术执行额度，不作为第二个钱包展示。

用户从 OPL 钱包中分配人民币 Gateway 预算。Ledger 必须先创建冻结，再向 Sub2API 增加技术额度。

针对一份计价快照：

```text
technical_credit_usd = held_cny / (usd_cny_rate * (1 + gateway_markup))
customer_charge_cny = actual_cost_usd * usd_cny_rate * (1 + gateway_markup)
```

每份计价快照记录：

- 货币对和汇率；
- Gateway 加价率；
- 生效时间和计价版本；
- 舍入规则；
- 冻结记录和账户引用。

Control Plane 通过具备幂等性的 Sub2API 余额 API 增加技术额度。Sub2API 在请求执行时扣减该额度。用量同步器使用重叠时间窗口读取用量记录，并根据 Sub2API usage ID 去重。

每条已结算 Ledger 记录保存：

- Sub2API usage ID 和 request ID；
- OPL 账户、用户、Key 和 Workspace 引用；
- 模型和 Token 数量；
- 原始 `actual_cost` 和美元金额；
- 计价快照；
- 最终人民币扣款；
- 幂等键和同步时间。

同步器结算 OPL 冻结，但不会反复覆盖 Sub2API 实时余额。对账流程比较预期剩余技术额度和 Sub2API 余额。出现实质性差异时，停止自动补充额度、触发运维告警，并保留已有的受限额度。

## 分阶段交付

### Phase 0：只读契约与安全边界

目的：证明 Console 可以安全观察现有 Gateway，同时不改变客户行为或 Sub2API 源码。

交付内容：

- 在 Sub2API 中创建 Control Plane 专用管理员；
- 配置管理员会话所需的部署 Secret；
- 在 Caddy 中限制管理 API 网络访问；
- 实现 Sub2API 登录、刷新、健康、版本、用户、Key 和用量客户端；
- 在 Console 中展示实时只读 Gateway 状态；
- 针对已部署 Sub2API 版本执行兼容性检查；
- 从仓库相邻运维文件中移除明文服务器凭据，并迁移到 SSH Key。

退出条件：

- Console 可以从 `https://gflabtoken.cn` 读取健康、版本、测试用户、Key 和用量；
- 浏览器状态、日志、收据和仓库文件中不出现任何 Secret；
- 现有 Gateway 流量和服务器更新流程保持不变。

### Phase 1：统一身份与 Gateway 入口

目的：在复用 Sub2API 门户的同时，消除第二套账户和密码体验。

交付内容：

- 为 Console 和 Sub2API 配置共享 OIDC 签发方；
- 在 Control Plane 中保存 `(issuer, subject)` 身份；
- 使用已验证邮箱绑定现有试点账户，并记录明确的审计证据；
- 延迟创建 Sub2API 用户并绑定 OIDC 身份；
- Console Gateway 操作通过 SSO 打开 `https://gflabtoken.cn`；
- 在 Sub2API 配置能力允许的范围内应用 OPL 品牌并隐藏支付入口。

退出条件：

- 已登录 Console 的用户无需 Sub2API 密码即可进入 Sub2API 用户门户；
- 同一个 OIDC subject 只能映射一个 OPL 用户和一个 Sub2API 用户；
- 已禁用 OPL 用户无法获得新的 Gateway 会话。

### Phase 2：Key 投影与 Workspace 注入

目的：让用户在现有门户管理 Key，并在本地 Codex 和 OPL Workspace 中使用，无需在 WebUI 输入 Key。

交付内容：

- 在 Console 中展示实时掩码 Key 列表；
- 提供不持久化的受保护 Key 显示操作；
- 展示 Codex Base URL 和模型配置；
- 保存 Workspace Key 选择和绑定记录；
- 由 Fabric 向各 Workspace Kubernetes Secret 注入 Key；
- 处理轮换、撤销、过期和异常绑定；
- 添加覆盖 Control Plane、Fabric 操作、Kubernetes 清单、日志和 API 错误的脱敏测试。

退出条件：

- 用户通过 SSO 创建 Key，在本地 Codex 使用，并为 Workspace 选择该 Key；Workspace 可以在 WebUI 不输入 Key 的情况下执行模型请求；
- 撤销 Key 后会阻止后续请求，并在 Console 中可见；
- Control Plane 数据库中不存在完整 Key。

### Phase 3：统一 Gateway 预算与 Ledger 结算

目的：让 OPL 钱包成为唯一客户余额，同时保留 Sub2API 的实时消费保护。

交付内容：

- Gateway 预算分配和 Ledger 冻结；
- 版本化的美元/人民币计价快照和 Gateway 加价率；
- 具备幂等性的 Sub2API 技术额度更新；
- 使用重叠窗口和去重的增量用量同步；
- 请求级 Ledger 记录和人类可读收据；
- Console 按天、模型、Key 和关联 Workspace 展示汇总与明细；
- 对账 Sub2API 用量、技术余额、Ledger 扣款和钱包冻结。

退出条件：

- 重复同步不能造成重复扣款；
- OPL 余额不足时不能创建新的 Gateway 技术额度；
- 每笔 Gateway 人民币扣款都能追溯到唯一 Sub2API usage ID 和计价快照；
- 试点窗口内对账结果不存在无法解释的用量或余额差异。

### Phase 4：独立更新兼容与生产发布

目的：保留现有的服务器优先 Sub2API 更新流程，同时避免静默破坏 Console 集成或计费。

交付内容：

- 从 Gateway 服务器运行的更新后兼容性检查命令；
- Console 持续观察健康状态和版本；
- 对管理员鉴权、用户查询、Key 列表、用量列表、用量统计和最小模型请求执行契约检查；
- 在 Console 管理页面显示兼容状态和最近检查时间；
- 有界失败行为：管理契约不兼容时停止新绑定和自动补充额度，同时保持现有数据面额度有上限；
- 运维告警、用量同步延迟告警、备份证据和对账运行手册；
- 先受控试点，再扩大发布范围。

退出条件：

- 在服务器更新 Sub2API 后，要么契约检查通过且无需发布 Console，要么明确报告不兼容；
- 不兼容更新不能造成无限、未计费的资金风险；
- 已实际演练回滚和数据库备份流程，并保存证据。

## 失败处理

- Gateway 不可用：Console 展示带最近成功同步时间的旧数据，并禁用变更操作。
- 管理员会话过期：Control Plane 尝试刷新一次，失败后关闭操作并告警。
- OIDC 映射冲突：禁止自动合并，必须由管理员审核。
- Key 获取或 Secret 更新失败：Workspace 绑定保持不变，操作记录脱敏后的失败信息。
- 用量同步超时：使用相同的 usage ID 去重规则重试重叠窗口。
- 缺少货币或计价配置：禁止分配技术额度或结算用量。
- 余额差异：停止补充额度，保留已有受限额度，并要求对账。
- Sub2API 契约不受支持：保留只读缓存投影，禁用新绑定和额度变更，并通知运维人员。

## 验证策略

各阶段的重点检查：

- 针对当前已部署 Sub2API 响应的 API 契约夹具。
- OIDC subject 唯一性、账户绑定、禁用用户和会话测试。
- Key 掩码、`no-store` 显示、日志脱敏和 Workspace Secret 测试。
- 通过 `https://gflabtoken.cn` 执行真实本地 Codex 和 Workspace 模型请求。
- 用量重叠、重复投递、乱序记录和金额舍入测试。
- 钱包冻结、余额不足、补充额度、余额差异和对账测试。
- 演练服务器端 Sub2API 更新，然后执行兼容性命令和 Console 验证。

生产验收链路：

```text
Console OIDC 登录
-> SSO 进入 gflabtoken.cn
-> 创建 API Key
-> 在本地 Codex 使用 Key
-> 为 Workspace 选择 Key
-> Workspace 模型请求
-> Sub2API 用量记录
-> Ledger 人民币结算
-> Console 用量和收据
-> Sub2API 服务器更新
-> 兼容性验证
```
