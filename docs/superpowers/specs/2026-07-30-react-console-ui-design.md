# OPL Cloud React Console UI Design

状态：`implemented-and-verified`

选定方向：方案 1，`Quiet Ledger` 独立 React Console。

不可变视觉基准：`output/imagegen/opl-console-option-1-quiet-ledger-1440x1024.png`，SHA-256 `9b75ba8b01cda552fcb7d44bc774797f22697f5fd4a0c067e885bc4895b738b5`。该文件只允许读取、比较和纳入版本控制，不得修改、重新生成或覆盖。

本设计固定 OPL Console 从 Vue 迁移到 React 的实现边界。业务展示内容仍由 [`docs/product/console-display-contract-v1.md`](../../product/console-display-contract-v1.md) 唯一定义；本文件只固定前端运行时、设计系统、组件边界、交互模式、测试策略和迁移完成条件。

## 1. 目标

在现有独立 Console 中完成一次行为保持的 Vue -> React 迁移，并交付一版可运行、可交互、可验证的 UI：

- 完整承载客户侧 5 个一级页面、Admin 额外 5 个一级页面和 27 个冻结 slide。
- 使用 React 和 TypeScript，不再包含 Vue 运行时、Vue SFC、Vue 构建插件或 Vue 类型检查器。
- 使用 `@openai/apps-sdk-ui` 提供的 React 基础组件和设计 tokens，但不把 ChatGPT iframe 的 inline/fullscreen 规则错误套用到独立 Console。
- 保留现有 Control Plane 产品 API、DTO、SourceEnvelope、状态机、幂等键和 Secret 安全语义。
- 把当前直接读取 `App.vue` 的语法测试迁移成 React 架构合同、纯模型测试和浏览器行为验收。

## 2. 不改变的业务真相

以下内容不是迁移变量：

- Browser 只调用 Control Plane 产品 API。
- Control Plane、Sub2API、Fabric、Ledger 的权威边界保持不变。
- `available`、`empty`、`unavailable` 语义保持不变；`unavailable` 不得显示为 `0` 或空列表。
- Workspace 价格、余额、生命周期和 provider 状态只展示服务端权威读回，浏览器不自行推导。
- API Key 和 Workspace 凭据默认隐藏；主动 reveal 后才可复制，并在 60 秒、离开敏感路由、切换账号或退出登录时清理。
- 现有命令的 Idempotency-Key、恢复入口和 manual review 允许动作保持不变。
- 不增加第二钱包、第二 Gateway、在线充值、公开注册、云厂商入口或 Sub2API 管理入口。
- 不在迁移基线中加入 ChatKit、Computer Use、运行时 GPT 请求或 ChatGPT MCP App。

## 3. 产品表面

### 3.1 独立 Console

React 应用继续由浏览器直接打开，并使用现有路由：

| 用户域 | 一级页面 | 固定路由 |
| --- | --- | --- |
| 客户 | 概览 | `/console/overview` |
| 客户 | Workspace | `/console/workspaces` |
| 客户 | API 服务 | `/console/api` |
| 客户 | 账单 | `/console/billing` |
| 客户 | 公告 | `/console/announcements` |
| Admin | 运维概览 | `/admin/overview` |
| Admin | 客户与计费账户 | `/admin/accounts` |
| Admin | 计费复核 | `/admin/billing` |
| Admin | 资源状态 | `/admin/resources` |
| Admin | 系统状态 | `/admin/system` |

Account Settings、Support、购买确认、进度、收据详情、资源详情、余额操作、复核处理和公告管理继续作为次级 slide、详情面板、抽屉或模态框，不增加一级导航。

### 3.2 ChatGPT App

本轮不实现 ChatGPT App。若以后增加，应作为独立 MCP Apps 表面，只承载适合对话的查询、比较、确认和跳转，不复制完整 Console。

## 4. 视觉与交互方向

### 4.1 固定视觉基准

方案 1 `Quiet Ledger` 是当前唯一实现目标，不再与旧的 B 方向或旧浏览器截图共同解释：

- 桌面基准 viewport 为 `1440x1024`。
- 左侧导航宽度 `240px`，使用浅色表面和轻量分隔线。
- 主画布为白色，不使用深蓝 Hero、渐变、装饰光斑或卡片堆叠。
- 深绿用于唯一主动作和健康状态；危险、警告和不可用仍使用各自语义色。
- 概览指标为开放式横向信息带，Workspace 是主要内容，最近账单和公告降低层级。
- 参考图中的现有 OPL Logo 和线性图标由仓库现有资产与图标库承载，不重新生成图片。

视觉基准不覆盖业务合同。参考图中的“运行中的 Workspace”在当前概览必须显示为 Control Plane 的 Workspace 总数或生命周期语义，不能暗示 Fabric Runtime 实时检查；参考图中的七日趋势在没有权威时间序列 DTO 时不得实现为假折线。

### 4.2 壳层

`Quiet Ledger` 使用安静、高密度、面向重复操作的云控制台壳层：

- 桌面端使用固定左侧导航和顶部账号区，内容区宽度充分用于表格和横向比较。
- 客户导航与 Admin 导航分组显示；非管理员完全看不到 Admin 入口。
- 移动端保留全部一级页面可达性；空间不足时使用抽屉或“更多”，不隐藏公告。
- 页面标题、来源级错误、权限状态和唯一主动作在首屏清晰可见。
- 页面 section 默认不做浮动卡片；卡片只用于独立对象、模态框和真正需要边界的工具面板。
- 表格使用紧凑行高、轻量分隔线和稳定列宽，移动端切换为可扫描的行列表。

### 4.3 视觉原则

吸收 OpenAI UI Guidelines 的通用原则：

- 使用系统字体栈，正文以 14–16px 为主，不用 viewport 宽度缩放字体。
- 通过间距、分组、对齐和字重建立层级；边框其次，阴影最后。
- 颜色用于动作、状态和有限品牌强调，不做大面积单色主题、渐变背景或装饰性光斑。
- 默认圆角不超过 8px；紧凑工具控件使用 4–6px。
- 每个 slide 只突出一个主动作；次要动作使用 outline、ghost、菜单或图标按钮。
- 保持 WCAG AA 对比度、键盘焦点、可读错误、可缩放文本和 reduced-motion 支持。

### 4.4 Apps SDK UI 的角色

`@openai/apps-sdk-ui` 是 React 基础组件来源，不是 Console 架构：

- 优先直接使用其 Button、Badge、Input、Textarea、Checkbox、Select、Tooltip、Popover/Menu、Avatar、Alert 和 loading primitives 中实际可用的导出。
- 在 `components/ui` 下建立薄适配层，统一 OPL 的尺寸、文案、状态色、表单错误和图标槽位。
- 表格、分页、SourceEnvelope 状态、Workspace 进度、Secret reveal、Admin 复核和资源详情属于 Console 专用组件，由本仓库实现。
- 若组件库缺少某个 Console 控件，使用原生语义元素和现有 tokens 补齐，不复制组件库内部代码。
- 图标迁移到 `lucide-react`，所有非显而易见图标按钮提供 tooltip 或 `aria-label`。

### 4.5 Workspace 开通视觉基准

Workspace 配置、核对和 operation 采用方案 1 `Split Decision`。不可变参考图为 `output/imagegen/opl-workspace-launch-option-1-split-decision-1440x1024.png`，SHA-256 `897f657539d2ccd8e10df365e5108094bee3a68ffd1c349190f692501657b4b6`；该文件只允许读取、比较和纳入版本控制，不得修改、重新生成或覆盖。

- 桌面为左侧决策面加右侧 `320px` sticky 订单摘要；小屏转为单列。
- 配置态编辑名称和套餐，核对态只读复核，operation 态只突出权威 `status` 和当前 `phase`。
- 三步条是页面导航状态，不是后端进度；禁止根据 phase 顺序推导此前节点已经完成。
- 价格目录、报价、余额和 operation 分别读取既有 Control Plane 产品 API；缺少 API 字段的区域、容量、ETA 和扣款后余额不展示。

## 5. React 架构

### 5.1 构建入口

- `apps/console-ui/src/main.tsx` 使用 `createRoot` 挂载 React。
- 根 `vite.config.ts` 使用 `@vitejs/plugin-react`，vendor chunk 识别 React、Apps SDK UI 和 icons。
- `apps/console-ui/tsconfig.json` 包含 `.ts`、`.tsx` 和 `.d.ts`，不包含 `.vue`。
- 根 `package.json` 使用 `tsc --noEmit` 进行 typecheck/lint 基线，并移除 Vue 依赖。
- Apps SDK UI 所需的 React 18/19 与 Tailwind 4 peer dependency 在根依赖中显式满足。

### 5.2 文件边界

迁移后的主要文件职责如下：

| 路径 | 职责 |
| --- | --- |
| `src/main.tsx` | React 挂载、Apps SDK UI Provider 和全局样式入口 |
| `src/App.tsx` | 顶层路由分派和 Session 门禁 |
| `src/app/console-router.ts` | history、popstate、已知路由和敏感路由判断 |
| `src/app/use-console-controller.ts` | 单一页面主控；Session、请求 generation、命令、幂等 intent、toast、全局 slide 和 Secret 生命周期 |
| `src/layout/ConsoleShell.tsx` | 桌面侧栏、顶部账号区、移动导航、G-ACC-01 和 G-SUP-01 |
| `src/pages/CustomerPages.tsx` | C-OV-01、C-WS-01 至 C-WS-05、C-API-01 至 C-API-02、C-BIL-01 至 C-BIL-03、C-ANN-01 |
| `src/components/keys/KeysPanel.tsx` | C-API-03 至 C-API-05 |
| `src/pages/AdminPages.tsx` | A-OV-01 至 A-OV-02、A-ACC-01 至 A-ACC-03、A-REC-01 至 A-REC-02、A-RES-01 至 A-RES-02、A-SYS-01 |
| `src/pages/PublicPages.tsx` | 公开边界页、登录、Session 恢复、403 和 404 |
| `src/components/source/SourceState.tsx` | available/empty/unavailable/loading/error 的统一表达 |
| `src/components/ui/*` | Apps SDK UI 薄适配层和 Console 专用基础控件 |
| `src/api/*` | 保留现有请求、DTO decode 和身份边界 |
| `src/console-model.ts` | 纯格式化、导航和状态映射函数 |

不引入 Redux、MobX 或第二套路由框架。当前规模使用一个 `useConsoleController` 主控，页面组件只消费经过 DTO 和 SourceEnvelope 约束的状态与命令；若未来拆分 hook，必须保持同一 Session、generation、幂等和 Secret 清理语义。

冻结的 27 个 slide 分组为：全局 2 个、客户 15 个、Admin 10 个。目录组织不改变 [`console-display-contract-v1.md`](../../product/console-display-contract-v1.md) 中的 slide ID、问题或动作合同。

### 5.3 数据流

每个页面遵守同一模式：

```text
route/session
-> page hook starts request generation
-> existing API adapter calls Control Plane
-> strict DTO / SourceEnvelope decode
-> stale generation result is discarded
-> SourceState renders available / empty / unavailable
-> command uses stable idempotency intent
-> authoritative readback replaces transient UI feedback
```

页面卸载或身份变化时，AbortController 或 generation guard 必须阻止旧请求覆盖新页面。错误只影响对应来源块，不把整个概览降级为单一失败页。

## 6. 关键交互合同

### 6.1 Workspace 购买

- 配置 slide 读取实时 catalog、preview 和 wallet。
- 配置和核对使用左决策、右订单摘要的 Split Decision 结构；右侧集中展示组成金额、总价、余额、自然月和续费意图。
- Basic/Pro 不可售时禁用并显示原因。
- 余额判断只控制交互提示；提交后由服务端重新校验。
- 确认 slide 完整展示套餐、规格、组成、总价、priceVersion、权益期和自动续费关闭。
- 用户确认后才允许提交；相同 intent 复用相同 Idempotency-Key。
- operation slide 只展示真实 status 和当前 phase，不推导逐阶段完成态，不显示虚假百分比，并提供服务端允许的动作。

### 6.2 API Key

- Key 列表不含 Secret。
- 创建/编辑使用真实 group 和限制字段。
- reveal 使用专用 no-store API；UI 只在当前账号、当前请求 generation 和当前敏感路由仍一致时接收结果。
- reveal 后显示倒计时语义、复制和主动隐藏；超时、退出、路由切换和 Session 变化清理内存。

### 6.3 Admin 命令

- 开通账号、余额调整、复核恢复、公告发布/撤下均使用模态框或详情面板。
- 模态框锁定背景滚动、支持 Escape、焦点陷阱和关闭后焦点恢复。
- 命令提交前显示操作对象和不可逆影响；完成后显示权威 readback，不从表单值推导结果。
- `acct-admin` 的保留保护在 UI 中可见，但仍由服务端最终拒绝非法动作。

## 7. 样式系统

现有 `tokens.css` 中与 OpenAI 指南一致的中性色、状态色、间距、圆角和 motion 可以保留并校准；删除 Vue transition class 和不再使用的组件样式。

样式分为：

- Apps SDK UI 和 Tailwind 4 基础入口。
- `tokens.css`：OPL 语义 token 映射。
- `components.css`：薄适配层和 Console primitives。
- `styles.css`：壳层、页面、表格和响应式布局。

不在 JSX 中堆叠大段一次性 style object，不使用 CSS-in-JS 运行时。

## 8. 测试迁移

### 8.1 删除的错误合同

删除或改名所有把 Vue 当业务真相的断言：

- “Console runtime is Vue without React”。
- `createApp(App).mount(...)`。
- `.vue` 文件必须存在。
- README 必须写 `Vue Console` 且拒绝 `React`。
- 直接依赖 Vue directive、template tag 或 SFC 文件名的正则。

### 8.2 保留和新增的合同

- `console-model` 纯函数测试保留，并改成框架中立文件名。
- API adapter、DTO、路由字符串、身份边界、Idempotency-Key 和 Secret no-store 测试保留。
- 新增 React 构建合同：React 入口、Vite React plugin、Apps SDK UI、无 Vue 依赖和无 `.vue` 文件。
- 新增组件行为测试：按钮 busy/disabled、表单错误、SourceState 三态、modal focus、segmented/select/checkbox 交互。
- 将客户/Admin flow 测试改为读取稳定的 feature boundaries 或运行浏览器行为，不再扫描一个巨型模板。
- 浏览器验收至少覆盖登录、客户五页、Admin 五页、Workspace 配置/确认/operation 的桌面双栏与移动单栏、无推导式进度、API Key reveal 清理、来源 unavailable 与移动导航。

### 8.3 TDD 顺序

先把框架合同改成 React 期望并在现有 Vue 基线上观察失败，再修改依赖、入口和组件。每个业务 feature 的 React 迁移先建立可失败的行为或边界测试，再删除对应 Vue 模板。

## 9. 文档和机器合同

本轮新增 `packages/contracts/opl-cloud-console-ui-contract.json`，固定：

- `framework=react`。
- `componentFoundation=@openai/apps-sdk-ui`。
- `surface=standalone_console`。
- Vue 运行时、Vue SFC、Vue plugin 和 Vue 语法测试为禁止项。
- 展示 SSOT 指向 `console-display-contract-v1.md`。
- API 和权威边界指向现有 source-truth、launch freeze 和 invariants。
- GPT、ImageGen 和 Computer Use 仅为设计/开发辅助，不是浏览器运行时依赖。

同步更新 README、`packages/README.md` 和 TKE 部署文档中的 Vue 描述。历史提交和已退休归档不做无关重写。

## 10. 迁移完成条件

只有同时满足以下条件才算完成：

1. 根依赖、Vite、TypeScript 和入口均为 React。
2. `apps/console-ui` 不再存在 `.vue` 文件或 Vue import。
3. React UI 完整覆盖冻结的 10 个一级页面和 27 个 slide。
4. `@openai/apps-sdk-ui` 的真实组件被生产 UI 使用，而不是只安装依赖。
5. 现有 API adapters/DTO 行为未改变，SourceEnvelope 与 Secret 合同通过测试。
6. Vue 语法绑定测试已删除或改成 React/行为合同。
7. typecheck、测试和 production build 通过。
8. 本地开发服务器可访问，桌面与移动关键路径经过浏览器验收。
9. 仓库当前文档和机器合同只把 React 描述为 Console 当前实现。
10. 最终 diff 不包含 `.superpowers/`、`.codegraph/`、截图缓存或生成临时文件。

## 11. ImageGen 与视觉验证

本轮已使用经用户授权的 ImageGen 设计辅助生成三套方向，用户选择方案 1 `Quiet Ledger`。选定文件固定为：

```text
output/imagegen/opl-console-option-1-quiet-ledger-1440x1024.png
sha256: 9b75ba8b01cda552fcb7d44bc774797f22697f5fd4a0c067e885bc4895b738b5
```

从选定时刻起，ImageGen 不再参与本轮实现；禁止重生成、编辑或覆盖该 PNG。实现只读取它作为视觉基准，并在每次 Design QA 前后复核 SHA-256。

浏览器验证必须在 `1440x1024` 同 viewport、已登录客户概览状态下，将该源图与实现截图放入同一比较输入。移动端另以 `390x844` 验证导航可达、无横向溢出、主动作和对象列表可操作。`design-qa.md` 只有在所有 P0/P1/P2 已关闭且写入 `final result: passed` 后才允许交付。

GPT、ImageGen、Computer Use 和 Product Design 仅为设计与开发辅助，不进入 Console 浏览器运行时，也不成为业务事实来源。
