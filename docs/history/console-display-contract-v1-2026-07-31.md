# Historical OPL Console UI Display Contract V1

State: `history_or_provenance`

This 2026-07-31 design freeze records the implementation context of PR #75.
It is not a current product, visual, navigation or test authority. The current
experience owner is
[`docs/product/console-experience-guide.md`](../product/console-experience-guide.md);
current routes and presentation live in `apps/console-ui`.

状态：`frozen-for-implementation`

本文件固定 OPL Console Pilot V2 的页面信息架构和展示内容。它回答“每个一级页面有哪些 slide、每个 slide 必须展示什么、事实来自哪里、用户可以做什么”。它不声明功能已经上线，也不改变 `code-complete`、`pilot-ready` 或 `production-proven` 状态。

约束来源：

- [`docs/invariants.md`](../invariants.md)
- [`console-workspace-v1.md`](../product/console-workspace-v1.md)
- [`opl-cloud-launch-freeze-contract.json`](../../packages/contracts/opl-cloud-launch-freeze-contract.json)
- [`opl-cloud-console-source-truth-contract.json`](../../packages/contracts/opl-cloud-console-source-truth-contract.json)
- [`opl-cloud-business-object-contract.json`](../../packages/contracts/opl-cloud-business-object-contract.json)
- [`opl-cloud-pricing-contract.json`](../../packages/contracts/opl-cloud-pricing-contract.json)

## 1. 固定的信息架构

客户侧只有五个一级页面：

1. 概览
2. Workspace
3. API 服务
4. 账单
5. 公告

管理员保留客户侧页面，并增加五个一级页面：

1. 运维概览
2. 客户与计费账户
3. 计费复核
4. 资源状态
5. 系统状态

本版本不新增其他一级导航。开户、余额操作、收据详情、资源详情、复核处理和公告管理以子 slide、抽屉或模态框承载。

Account Settings 和 Support 保留为全局账号/帮助菜单下的次级 slide，不占用一级导航。

本文中：

- “页面”指一级导航入口。
- “slide”指一个具有独立用户问题和明确动作的稳定界面状态，可以是路由、页内 Tab、详情面板、抽屉或模态框。
- “主动作”指当前 slide 最重要且唯一突出的下一步动作。

## 2. 产品展示原则

每个 slide 必须让用户明确知道：

1. 当前对象是什么。
2. 当前事实是什么。
3. 事实是否可用、来自哪里、何时读回。
4. 当前能执行什么动作。
5. 当前不能执行时，原因和下一步是什么。

### 2.1 权威来源

| 事实 | 唯一权威来源 | Console 的职责 |
| --- | --- | --- |
| Account、Session、Workspace 生命周期、价格快照、权益期、续费状态 | Control Plane | 投影和导航 |
| 可用余额、Endpoint、API Key、请求用量、实际费用、余额历史 | Sub2API | 通过 Control Plane 展示和操作 |
| Compute、Storage、Attachment、Runtime、挂载和 provider 实时状态 | Fabric | 展示实时读回，不保存副本 |
| Receipt、审计、复核和 reconciliation 证据 | Ledger | 展示追加式证据，不推导余额 |
| 页面状态、筛选、表单、短暂 reveal 的 Secret | Browser | 不成为业务事实来源 |

浏览器只调用 Control Plane 产品 API，不直连 Sub2API、Fabric、Ledger 或腾讯云。

### 2.2 来源状态

每个远端数据块必须支持：

| 状态 | 含义 | UI 行为 |
| --- | --- | --- |
| `available` | 权威来源读取成功且有数据 | 展示真实字段和读回时间 |
| `empty` | 权威来源读取成功且确实为零行 | 展示业务空态，可以显示真实的 `0` |
| `unavailable` | 来源失败或事实无法确认 | 展示“暂不可用”和重试，禁止伪装为 `0` 或空列表 |

不同来源独立失败。例如余额不可用时，Workspace 列表仍可展示；Ledger 不可用时，不能隐藏已有 Workspace 条款。

### 2.3 金额、时间和状态

- 客户金额统一显示 USD，底层使用整数 USD micros。
- Workspace 总价、组成和价格版本只展示服务端报价或快照，前端不自行定价。
- Provider 的 CNY 成本、汇率和内部成本公式禁止出现在客户页面。
- 时间显示用户可读时区，同时保留可用于支持定位的精确时间。
- `fetchedAt` 是本次读回完成时间，不得冒充来源更新时间。
- 未知状态显示“暂不可用”，不得映射成“正常”。

### 2.4 Secret

- 密码和 Key 默认隐藏，列表 DTO 不包含 Secret。
- 只有用户主动 reveal 后才展示和允许复制。
- reveal 响应使用 private/no-store，并在离开敏感页面、切换账号、退出登录或 60 秒后清除。
- 页面、日志、Receipt、审计和错误信息不得保存原始密码、API Key 或 provider Secret。

### 2.5 视觉呈现冻结

当前固定视觉方向为方案 1 `Quiet Ledger`。不可变参考图为 `output/imagegen/opl-console-option-1-quiet-ledger-1440x1024.png`，SHA-256 为 `9b75ba8b01cda552fcb7d44bc774797f22697f5fd4a0c067e885bc4895b738b5`；该文件不得修改、重新生成或覆盖。

视觉合同固定：

- 桌面使用 `240px` 浅色左侧导航、白色全宽内容画布和轻量分隔线。
- 深绿只承担主要动作和健康状态；不用深蓝 Hero、渐变、装饰光斑或卡片堆叠。
- 概览使用开放式指标带，Workspace 为主要内容，账单和公告为次级内容。
- 内部 Slide ID 只保留为机器可验收的 `data-slide`，不得作为客户或管理员可见文案。
- UI 不展示解释实现边界的教学句；来源、可用性和读回时间使用对应数据块的状态组件表达。
- 参考图不覆盖权威来源合同。Control Plane Workspace 总数不得写成 Fabric Runtime 运行数；没有权威时间序列 DTO 时不得绘制 API 趋势。

### 2.6 全局壳层

所有登录后页面固定展示：

- OPL Cloud 产品标识。
- 当前页面标题。
- 当前登录邮箱、角色和账号菜单。
- 客户五项导航；管理员额外展示 Admin 五项导航。
- 退出登录。
- 当前页级加载、错误、重试和权限状态。

移动端必须能到达全部一级页面。底部导航放不下时，公告进入“更多”菜单，但不能成为不可达页面。

不展示：公开注册、支付订单、在线充值、第二个钱包、Sub2API 管理入口、云厂商控制台入口、备份恢复、文件浏览、文件系统用量、协作和 GPU/HA 能力。

### 2.7 全局次级 slide

#### G-ACC-01 Account Settings

账号菜单中的只读账号设置展示：

- 登录邮箱。
- Account ID。
- Console User ID。
- 角色。
- Sub2API User ID 映射。
- 账户状态。
- Session 到期时间。
- 退出登录。

当前 Pilot 不提供客户自行注册、修改邮箱、重置密码、切换 Account 或管理成员。密码权威属于 Sub2API，Console 不展示本地密码设置。

#### G-SUP-01 Support

帮助菜单中的支持 slide 展示当前 Account 的外部工单映射：

- 标题。
- 外部工单系统和工单号。
- 外部工单链接。
- 状态、分类和优先级。
- 关联 Workspace、资源和 operation。
- 创建时间、更新时间和已有消息。

新增映射时展示外部工单号、标题、外部链接、问题说明，以及可选 Workspace、资源和 operation 上下文。当前 API 只把已存在的外部工单映射到 OPL 上下文，不得把该动作描述成“在 Console 内创建工单”。

## 3. Slide 总表

| ID | 一级页面 | Slide | 路由或形态 | 用户要回答的问题 |
| --- | --- | --- | --- | --- |
| G-ACC-01 | 全局 | Account Settings | 账号菜单/抽屉 | 当前账号和 Session 是什么？ |
| G-SUP-01 | 全局 | Support | 帮助菜单/抽屉 | 当前外部支持工单关联了哪些 OPL 对象？ |
| C-OV-01 | 概览 | 账户概览 | `/console/overview` | 我现在有什么、能做什么？ |
| C-WS-01 | Workspace | Workspace 列表 | `/console/workspaces` | 我有哪些 Workspace？ |
| C-WS-02 | Workspace | 新建配置 | `/console/workspaces/new` 配置态 | 我要买哪个套餐？ |
| C-WS-03 | Workspace | 购买确认 | `/console/workspaces/new` 确认态 | 本次到底买什么、扣多少钱？ |
| C-WS-04 | Workspace | 开通进度与结果 | `/console/workspaces/new` operation 态 | 开通到了哪一步、是否需要处理？ |
| C-WS-05 | Workspace | Workspace 详情 | `/console/workspaces/:id` | 现在能否打开，访问凭据和权益是什么？ |
| C-API-01 | API 服务 | API 概览 | `/console/api` | 余额和本月 API 消费如何？ |
| C-API-02 | API 服务 | 使用记录 | `/console/api/usage` | 哪个 Key 在何时产生了多少真实费用？ |
| C-API-03 | API 服务 | API Key 列表 | `/console/api/keys` | 有哪些 Key，状态和限制是什么？ |
| C-API-04 | API 服务 | Key 创建/编辑 | 模态框 | 如何创建或修改普通 Key？ |
| C-API-05 | API 服务 | Key reveal/使用/删除 | 行展开或模态框 | 如何安全使用或删除 Key？ |
| C-BIL-01 | 账单 | Workspace 条款 | `/console/billing` 条款 Tab | 每个 Workspace 的当前商业条款是什么？ |
| C-BIL-02 | 账单 | 账单收据 | `/console/billing` 收据 Tab | 发生过哪些购买、续费、到期或退款？ |
| C-BIL-03 | 账单 | 收据详情 | 详情面板 | 这笔账的周期、金额和组成是什么？ |
| C-ANN-01 | 公告 | 公告列表 | `/console/announcements` | 当前有哪些有效通知？ |
| A-OV-01 | 运维概览 | 运营总览 | `/admin/overview` | 系统现在最需要处理什么？ |
| A-OV-02 | 运维概览 | 公告管理 | 页内区块/模态框 | 当前公告处于什么状态，是否发布或撤下？ |
| A-ACC-01 | 客户与计费账户 | 账户列表 | `/admin/accounts` | 每个客户的映射、余额和使用事实是什么？ |
| A-ACC-02 | 客户与计费账户 | 开通账户 | 模态框 | 如何创建管理员预配置账号？ |
| A-ACC-03 | 客户与计费账户 | 账户与余额操作 | 详情/模态框 | 对哪个账户做什么余额操作，结果是否确认？ |
| A-REC-01 | 计费复核 | 复核队列 | `/admin/billing` | 哪些操作或资源存在不一致？ |
| A-REC-02 | 计费复核 | 复核详情与恢复 | 详情/模态框 | 权威事实是什么，服务端允许哪个动作？ |
| A-RES-01 | 资源状态 | Workspace 资源列表 | `/admin/resources` | 每个 Workspace 与资源的当前事实是什么？ |
| A-RES-02 | 资源状态 | 资源详情 | 页内详情 | 某个 Workspace 的 Compute、Storage、Attachment 是否一致？ |
| A-SYS-01 | 系统状态 | 服务健康 | `/admin/system` | Control Plane、Sub2API、Fabric、Runtime、Ledger 是否可用？ |

## 4. 客户页面

### 4.1 概览

#### C-OV-01 账户概览

页面目标：让用户在首屏确认账户是否可用，并得到一个明确下一步。

首屏必须展示：

- 可用余额，来源 Sub2API。
- 本月 API 实际费用，来源 Sub2API 账号级汇总。
- 本月 API 请求数，桌面空间足够时展示；移动端可进入 API 概览查看。
- Workspace 总数，来源 Control Plane 分页总数。
- 一个主动作：打开可用 Workspace、新建 Workspace、查看 Workspace 或重试 Workspace 读取。

Workspace 摘要展示：

- 名称和 Workspace ID。
- 套餐。
- Control Plane 生命周期状态。
- `paidThrough`。
- 进入详情的动作。

这里的状态必须标注为“生命周期状态”，不能暗示已经完成 Fabric 实时可用性检查。能否真正打开只在 Workspace 详情页通过 Runtime 实时读回确认。

最近账单展示最近三条：

- 类型。
- 时间。
- 金额。
- 状态。
- 进入账单页的动作。

公告摘要展示最近三条有效公告：

- 标题。
- 发布时间。
- 正文摘要。
- 已读/未读。
- 标记已读或进入公告页。

来源分别失败时独立展示“暂不可用”和重试，不得阻塞整个概览。

概览不展示七日趋势或折线图，直到 Control Plane 提供来自 Sub2API、范围和时间窗明确的权威时间序列 DTO。当前账号级汇总只能展示余额、本月实际费用、本月请求数和 Workspace 总数。

### 4.2 Workspace

#### C-WS-01 Workspace 列表

必须展示：

- Workspace 总数。
- 名称和 Workspace ID。
- Basic/Pro 套餐。
- 生命周期状态。
- `paidThrough`。
- 分页。
- 新建 Workspace 主动作。
- 非终态 launch operation 的恢复提示和进度入口。

空态为“暂无 Workspace”，主动作是“新建 Workspace”。`unavailable` 时只允许重试，不能展示空态。

#### C-WS-02 新建配置

采用方案 1 `Split Decision`：左侧只承载名称和套餐决策，右侧使用固定宽度订单摘要集中展示价格、余额、周期和主动作。桌面摘要随页面滚动保持可见；窄屏改为单列并放在配置之后。三步条只表示“配置 / 核对 / 开通状态”的页面导航位置，不表示后端阶段完成情况。

必须展示：

- Workspace 名称输入。
- Basic 和 Pro 套餐选择。
- 套餐是否当前可售；不可售套餐必须禁用并说明暂不可用。
- CPU、内存和持久存储规格。
- 计算月费、存储月费和 Workspace 月度总价。
- 计费单位为一个自然月。
- 当前可用余额。
- 自动续费关闭。
- 进入确认 slide 的主动作。

当前价格合同如下，仅作为当前 `priceVersion` 的展示基准，UI 仍必须使用服务端目录和报价：

| 套餐 | 计算规格 | 存储 | 计算组成 | 存储组成 | 单次月度总额 |
| --- | --- | --- | --- | --- | --- |
| Basic | 2 vCPU / 4 GB | 10 GB | $50.00 | $2.58 | $52.58 |
| Pro | 8 vCPU / 16 GB | 100 GB | $214.28 | $25.80 | $240.08 |

计算和存储组成只解释履约内容；客户每个权益期只发生一次 Workspace 总额扣款，不能显示成两次独立收费。

余额不足时：

- 明确展示可用余额和所需总额。
- 禁用继续购买。
- 提示联系管理员处理余额。
- 不提供在线支付或充值入口。

浏览器中的余额充足提示只用于交互反馈：只有 `wallet.data.usdMicros` 严格大于 `preview.totalChargeUsdMicros` 才允许进入核对和提交，余额等于总额仍视为不足。提交后服务端必须重新读取余额并按同一合同校验，客户端判断不能授权扣款。

字段审计固定如下：

| 展示内容 | Control Plane 产品 API | 使用字段 | 规则 |
| --- | --- | --- | --- |
| 套餐、可售性、CPU、内存、存储 | `GET /api/pricing/catalog` | `packages[].id/name/available/cpu/memoryGb/diskGb` | 不可售即禁用，不把 catalog 可售解释成腾讯容量 |
| 计算组成、存储组成、总价、价格版本、币种、周期 | `POST /api/pricing/preview` | `compute.chargeUsdMicros`、`storage.chargeUsdMicros`、`totalChargeUsdMicros`、`priceVersion`、`currency`、`billingUnit` | 只格式化，不在浏览器相加或定价 |
| 可用余额 | `GET /api/gateway/wallet` | `data.usdMicros`、`data.currency` 及 SourceEnvelope 状态 | 不可用时显示“暂不可用”，不回退为 `0`；提交提示使用余额严格大于总额 |
| 名称、套餐、容量、续费意图 | `POST /api/workspace-launches` 请求 | `name`、`packageId`、`sizeGb`、`autoRenew` | 它们是用户输入；配置态不是服务端既成事实 |

区域、即时容量、预计耗时和扣款后余额没有当前客户 API 字段，因此不得展示。

#### C-WS-03 购买确认

保持与配置态相同的左右分栏和订单摘要，左侧从编辑转为只读核对。确认页不得引入新的业务字段；名称和续费来自待提交表单，套餐规格来自 catalog，金额、周期和价格版本来自 preview，余额来自 wallet。

用户提交前必须完整展示：

- Workspace 名称。
- 套餐名称。
- CPU、内存、存储规格。
- 计算组成金额。
- 存储组成金额。
- 服务端权威总价。
- 价格版本。
- 当前可用余额。
- 权益期：一个自然月。
- 自动续费关闭。
- “一次性扣除 Workspace 月度总额，计算和存储包含在内”的明确说明。
- 用户确认复选框。
- 返回修改和“确认预付并开通”。

前端不得自行计算扣款后余额；只有服务端返回权威 readback 时才可展示。

#### C-WS-04 开通进度与结果

必须展示：

- operation ID。
- Workspace 名称和套餐。
- 用户可读状态和原始稳定状态码。
- 当前 phase 和用户可读阶段。
- 创建时间、最后更新时间。
- `errorCode`，但不展示 raw provider 响应。
- 当前允许的动作。

operation 区域只读取 `GET /api/workspace-launches/{operationId}`。`operationId/status/phase/name/packageId/priceVersion/currency/totalChargeUsdMicros/autoRenew/createdAt/updatedAt/errorCode` 均直接来自该响应；中文阶段名只是 `phase` 稳定码的展示映射，不是新事实。

只突出当前 `phase`，不得依据阶段顺序把此前节点推导为“已完成”，也不展示逐节点完成态、虚假百分比、区域、容量或预计耗时。服务端返回下一次状态前，页面保持当前权威 readback，并允许刷新同一 operation，禁止重复购买。

终态及动作：

| 状态 | 展示 | 允许动作 |
| --- | --- | --- |
| `succeeded` | 已开通 | 查看 Workspace |
| `refunded` | 未完成且已退款 | 返回列表或查看账单 |
| `failed` | 已确认失败 | 返回列表或按错误码处理；不得自行推断扣款结果 |
| `manual_review` | 正在人工复核 | 查看 operation，禁止重复购买 |
| `unknown` / 轮询异常 | 结果待确认 | 恢复同一 operation 或刷新，禁止新提交 |
| `waiting` / `retryable` | 继续处理中 | 刷新同一 operation |

余额不足不得进入 Fabric 写路径；部分或未知资源不得承诺自动退款；激活后 Receipt 失败不得把 Workspace 显示为开通失败。

#### C-WS-05 Workspace 详情

页面目标：在一个位置回答 URL、用户名、密码和对应 Workspace Key，并明确现在是否真的可打开。

身份区展示：

- 名称。
- Workspace ID。
- Control Plane 生命周期状态。
- Fabric Runtime 实时状态。
- 刷新动作。

访问与凭据区展示：

- Runtime 是否 ready。
- 挂载检查结果。
- 服务健康结果。
- Workspace URL。
- 用户名。
- 密码 reveal/hide/copy。
- Workspace Key reveal/hide/copy。
- 轮换密码。
- 只有 Runtime `running + ready + URL` 同时成立时启用“打开 Workspace”。

套餐与条款区展示：

- Basic/Pro。
- CPU、内存和存储规格。
- Workspace 月度总价。
- 价格版本。
- 创建时间。
- `periodStart`。
- `paidThrough`。
- 续费状态。
- 自动续费关闭及不可启用原因。

不展示项目目录、文件列表、文件系统用量、原始 provider 信息或 Runtime Secret 引用。

### 4.3 API 服务

API 服务固定为三个页内 Tab：概览、使用记录、API Key。

#### C-API-01 API 概览

必须展示：

- 可用余额。
- 本月实际费用。
- 本月请求次数。
- 分页余额历史。

余额历史每行展示：

- 时间。
- 类型。
- 带正负号的金额。
- 状态。

这里展示 Sub2API 钱包和 API 消费，不展示 Workspace Receipt，也不提供充值按钮。

#### C-API-02 使用记录

必须展示：

- API Key 选择器。
- 周期选择：今日、本周、本月。
- 汇总请求次数。
- 汇总总 Token。
- 汇总实际金额。
- 分页请求记录。

每条请求固定按以下顺序展示：

- 模型与入站 Endpoint。
- Token：输入、输出、缓存读取、缓存写入。
- 费用：仅展示 `actualCostUsdMicros` 对应的实际金额。
- 延迟：首字 `firstTokenMs` 和总耗时 `durationMs`。
- 时间。
- 请求 ID。

Token、费用、延迟和时间均来自 Sub2API 请求记录，不能由当前页汇总、估算或反推。缺失的延迟显示 `-`，不得显示 `0 ms`。当前版本不展示 prompt、response、原始请求指纹或 provider 凭据；汇总 Token 仍来自 Sub2API 聚合接口，不由当前页相加。

#### C-API-03 API Key 列表

页头必须展示：

- 公共模型 Endpoint `https://gflabtoken.cn/v1` 的 Control Plane 投影。
- 复制 Endpoint。
- 创建普通 Key。
- 刷新。

列表支持服务端分页以及名称/ID、分组、状态、排序和每页数量控制。不能为搜索而在浏览器扫描所有上游页面。

每个 Key 展示：

- 名称、Key ID。
- 普通 Key 或 Workspace 系统 Key。
- 分组及平台信息。
- 状态。
- 当前并发。
- 总配额和已用金额。
- 5 小时、1 天、7 天消费限额及已用金额。
- 过期时间。
- 最近使用时间；允许展示客户自己的最近使用 IP。
- 创建时间。
- 当前允许的操作。

Workspace 系统 Key 可以 reveal 和查看使用说明，但不能被客户改组、停用、重置或删除。

#### C-API-04 Key 创建/编辑

创建普通 Key 展示：

- 名称。
- 必选分组及平台/倍率/状态说明。
- 总配额，`0` 表示不限。
- 有效天数。
- 5 小时、1 天、7 天消费限额。
- IP 白名单和黑名单。
- 创建确认。

编辑展示相同限制和明确的过期时间。完整 Key 不进入创建响应或列表 DTO；创建完成后通过专用 reveal 结果展示并提供复制，后续再次查看仍使用 reveal 命令。

#### C-API-05 Key reveal/使用/删除

reveal 展示完整 Key、复制和 60 秒自动隐藏。

使用说明展示：

- Endpoint。
- 分组平台。
- 当前 Key。
- 可复制的最小配置示例。

普通 Key 可执行：编辑、启用/停用、换组、重置配额用量、重置消费限额用量和删除。删除必须二次确认并明确不可恢复。

### 4.4 账单

账单固定为两个页内 Tab：Workspace 条款、账单收据。

#### C-BIL-01 Workspace 条款

每个 Workspace 展示：

- 名称和 Workspace ID。
- 套餐。
- 月度总价。
- `periodStart` 至 `paidThrough`。
- 续费状态。
- 自动续费状态。
- 进入 Workspace 详情。

这些是 Control Plane 当前商业条款，不是 Ledger Receipt，也不是 Sub2API 余额历史。

#### C-BIL-02 账单收据

列表展示：

- 时间。
- 类型：开通、续费、到期、退款。
- Workspace。
- 总额或退款额。
- 状态。
- 查看详情。
- Ledger cursor 分页。

Ledger 不可用时显示“账单收据暂不可用”，不能用 Workspace 条款合成一张假收据。

#### C-BIL-03 收据详情

必须展示：

- Receipt ID。
- 类型和状态。
- 创建时间。
- Workspace ID。
- 总额。
- 退款额，仅退款类型显示。
- 计费周期。
- 价格版本。
- 计算组成金额。
- 存储组成金额和容量。
- 可用于支持定位的 charge reference。

不向客户展示 provider ID、原始 Ledger 载荷或内部履约操作细节。

### 4.5 公告

#### C-ANN-01 公告列表

只展示当前用户有权看到且处于有效发布时间窗内的已发布公告：

- 标题。
- 完整纯文本正文。
- 发布时间。
- 已读/未读。
- 标记已读。
- 刷新。

不展示草稿、已排期但未开始、已撤下或过期公告。空态与不可用态必须区分。

## 5. Admin 页面

Admin 页面只供 operator 使用。Admin 可以进入客户侧页面查看自己的客户视图，但 Admin 数据和动作不得出现在普通客户路由中。

### 5.1 运维概览

#### A-OV-01 运营总览

首屏展示：

- 计费账户总数、正常数、停用数。
- Workspace 总数。
- 资源总数。
- 待复核数。
- 总体健康状态。

只有 `/api/operator/overview` 返回权威、范围明确且有界的聚合时，才额外展示：

- 汇总余额。
- Key 总数。
- 今日和累计 API 实际费用。

不得把“客户与计费账户”当前页的数据相加后冒充全局聚合。聚合来源不可用时显示来源问题，不显示 `0` 或不完整总额。

注意事项区按优先级展示：

1. billing reconciliation 全局 mismatch 或开通阻断。
2. `manual_review` 项。
3. 服务不健康。
4. 权威来源不可用。

页面主动作进入最高优先级问题对应页面，不在未选择账户时提供全局余额操作。

来源状态表展示：

- 来源名称。
- `available` / `empty` / `unavailable`。
- `fetchedAt`。
- 权威 `sourceUpdatedAt`，仅来源真实提供时显示。

#### A-OV-02 公告管理

公告管理作为运维概览内的区块，不新增一级导航。

每条公告展示：

- 标题。
- 正文。
- 草稿、已排期、已发布或已撤下。
- 开始和结束时间。
- 最后更新时间。
- 发布或撤下动作。

新建公告展示标题、纯文本正文、可选开始和结束时间，先保存草稿。发布和撤下均需明确确认并写入审计。

### 5.2 客户与计费账户

#### A-ACC-01 账户列表

桌面使用单个密集表格，固定为七列：

- 用户：登录邮箱和角色。
- 账户映射：OPL Account ID、Console User ID、Sub2API User ID。
- 余额：可用余额和钱包状态。
- API 费用：今日和累计实际费用。
- 资源：Key 数和 Workspace 数。
- 状态：账户状态。
- 操作：查看账户、余额操作和停用普通客户。

移动端使用紧凑账户卡，并保持相同的信息顺序。嵌套来源的可用性和读回时间不占用默认列表列，统一进入账户详情。

列表先按 Control Plane 稳定分页，再只对当前页做 Sub2API/Fabric 聚合。当前 API 没有搜索或排序合同，因此页面不展示搜索或排序控件，也不得为了筛选下载 1000 个账户。

允许动作：查看账户、余额操作、停用普通客户。保留管理员账户只读，不允许自停用。

#### A-ACC-02 开通账户

展示：

- 登录邮箱。
- 初始密码。
- 姓名。
- 明确说明将创建 Account、User、Sub2API 身份及映射。
- 提交状态、operation ID、phase、结果和错误码。

成功后必须读回账户映射，不能仅凭 POST 返回 toast 宣称开通完成。

#### A-ACC-03 账户与余额操作

账户详情至少展示：

- Account、Console User、Sub2API User 三方映射。
- 邮箱、角色和状态。
- Sub2API 钱包余额与状态。
- Key、Usage 和 Workspace 汇总。
- 每个嵌套来源的状态和读回时间。
- 关联的 Support 工单映射。
- 相关来源状态和读回时间。

余额操作固定支持：充值、扣减、业务退款。

提交前展示：

- 目标 Account ID，要求再次输入或明确确认。
- 操作类型。
- USD 金额。
- 业务原因。
- 可选关联 operation ID。
- 二次确认。

提交后展示：

- operation ID。
- 状态和 phase。
- 操作前余额。
- 操作后余额。
- 余额历史引用。
- Receipt ID，如有。
- actor。
- 上游失败 phase、HTTP status、errorCode、requestId。
- 服务端 `allowedActions`。

`manual_review` 只显示服务端允许的 `recover_wallet_adjustment`，并要求 `evidenceRef`。不得重复创建一笔新余额操作来“修复”未知结果。

### 5.3 计费复核

#### A-REC-01 复核队列

每行展示：

- Account ID。
- 资源类型。
- 当前状态。
- billing operation ID。
- phase。
- errorCode。
- operation reference。
- Receipt reference。
- 服务端 `allowedActions`。

支持状态、资源类型和 Account 维度的服务端筛选与分页；若后端尚未提供对应查询参数，UI 不得前端扫描所有页模拟筛选。

空态为“暂无待复核项目”；来源失败为“复核数据暂不可用”。

#### A-REC-02 复核详情与恢复

详情按来源分栏展示：

- Control Plane：Account、Workspace、billing operation、报价、扣款意图、phase、errorCode。
- Sub2API：权威余额和对应余额历史证据。
- Fabric：Compute、Storage、Attachment 的存在性、身份、规格、Zone、状态和最近读回。
- Ledger：Receipt 或 reconciliation exception。

当前 projection 未返回的事实显示“暂不可用”，不得从其他来源猜测。

动作规则：

- `workspace.launch.v2 + manual_review` 只显示 `diagnose_workspace_recovery_plan`，该动作进入“诊断 -> 查看服务端 Recovery Plan -> zero-mutation Validate -> 确认继续”，不调用旧 `/recover`。
- Workspace 续费或历史资源 `manual_review` 只显示 `resolve_billing_review`。
- 全局 `BillingReconciliation_mismatch` 只展示阻断和证据，不提供自动修复。
- 每次处理要求 `evidenceRef`、二次确认和审计。
- 未出现在 `allowedActions` 中的动作不渲染。

不展示 raw Sub2API/Fabric/Ledger 响应，不允许 operator 自由拼接退款、重购、删除或激活动作。

### 5.4 资源状态

#### A-RES-01 Workspace 资源列表

Workspace 行展示：

- Workspace 名称和 ID。
- owner Account。
- owner User 邮箱。
- 套餐和月度总价。
- 创建时间。
- `paidThrough`。
- 续费状态。
- Workspace 生命周期状态。
- URL。
- Receipt ID。
- Workspace Key 累计实际费用。
- 查看资源详情。

Control Plane 分页先完成，再只为当前页做 Fabric/Sub2API 聚合。

#### A-RES-02 资源详情

每个 Compute、Storage、Attachment 资源展示：

- owner Account。
- owner User。
- Workspace。
- 资源类型。
- 套餐或规格。
- provider ID。
- Zone。
- 实时状态。
- 创建时间。
- 到期时间。
- 最近 provider 读回时间。
- operation reference。
- Receipt reference。

字段必须保持各自 SourceEnvelope；Fabric 或 Ledger 不可用时不能回退到 Control Plane 缓存状态。

Admin 可以查看 provider ID 和结构化资源事实，但不展示 provider 凭据、原始 API 响应、云账号 Secret、任意删除按钮或通用 Fabric API。

### 5.5 系统状态

#### A-SYS-01 服务健康

固定展示五个服务域：

1. Control Plane。
2. API 服务 / Sub2API Gateway。
3. Fabric 资源服务。
4. Workspace Runtime 服务。
5. Ledger 账单记录。

每行展示：

- 服务名称。
- 正常、需处理或暂不可用。
- readiness 生成时间。
- Console 读回时间。
- 失败时的客户影响范围。
- 刷新。

总体状态按最差真实状态计算；任何必要来源 `unavailable` 时不能显示全绿。

`code-complete`、`pilot-ready`、`production-proven` 属于发布证据，不得从普通服务健康推导。除非后端提供对应的不可篡改发布证据 DTO，否则系统状态页不显示或声称这些级别。

## 6. 页面之间的业务链

```text
Admin 开通账户
→ 客户登录
→ 概览确认余额与下一步
→ Workspace 选择套餐
→ 确认一次月度总额
→ operation 开通与恢复
→ Workspace 详情确认 Runtime 可用并获取凭据
→ 外部使用 Workspace 或 API
→ API 服务查看 Key、Usage 和余额历史
→ 账单查看 Workspace 条款和 Ledger Receipt
→ 公告接收运营通知
```

异常链：

```text
余额不足
→ 不写 Fabric

扣款或外部结果未知
→ 恢复同一 operation
→ 禁止重复提交

确认 Compute 与 Storage 均不存在
→ 一次幂等 Workspace 退款

存在部分资源或身份无法确认
→ manual_review
→ Admin 计费复核

Workspace 已激活但 Receipt 缺失
→ Workspace 保持可用
→ 只重试 Receipt
```

## 7. 当前实现状态与权威限制

当前 React Console 已在隔离 worktree 中达到 `code-complete`，并由合同测试、单元测试和本地 fake-only Browser QA 验证以下展示合同：

- Workspace 购买确认展示 CPU/内存、计算/存储组成、价格版本、月度总额和明确权益期；字段来自产品合同与服务端价格预览。
- Workspace 详情展示价格版本和 `periodStart`。CPU/内存仅在权威详情源提供时展示，否则明确显示“暂不可用”，不得根据套餐在浏览器中反推。
- 收据详情展示 Receipt ID、计算/存储组成和 charge reference，并隐藏履约资源与 provider 内部字段。
- 客户与计费账户提供完整账户详情、开户 operation 结果和账户列表权威读回。
- 计费复核按 Gateway、Fabric、Ledger 来源组织证据；恢复动作只按服务端 `allowedActions` 展示。
- 运维概览消费 Control Plane 返回的全局 Sub2API 余额、Key 和 Usage 聚合；来源不可用时保留“暂不可用”，不得包装成运营指标。
- 系统状态已提供“客户影响范围”列；健康源未提供影响事实时显示“暂不可用”，不得由前端推断。
- 移动端公告通过客户概览中的“全部”入口明确可达，不增加冻结合同以外的一级底部导航。
- Account Settings 和 Support 已作为全局次级 slide 实现，不新增一级导航。

上述证据只证明本地实现与 fake 数据链路，不代表真实认证后运行、生产数据完整性或生产部署已经验证。
