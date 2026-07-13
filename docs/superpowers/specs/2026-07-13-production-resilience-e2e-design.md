# 生产浏览器与韧性验收设计

## 目标

在不修改 `one-person-lab-app` 的前提下，为 OPL Cloud 增加三组可重复、可清理的生产证据：

1. Console 页面真实点击完成资源商业生命周期；
2. 腾讯云同时开通 5 台 Basic CVM，并证明 Machine 原子归属；
3. 只针对 verifier 自有资源执行可控故障，证明幂等恢复、运行时恢复、停费和 Hold 释放。

现有单链生产 E2E 继续作为日常回归。并发 soak 和故障演练使用独立的手动 workflow，
不会随普通 push 自动产生云费用。

## 责任边界

浏览器回复误判属于 OPL Cloud verifier。`one-person-lab-app` 将用户首句显示为会话标题和
侧栏条目是正常产品行为；OPL Cloud 不能用整页字符串计数推断 assistant reply。

本设计禁止修改、提交或推送 `one-person-lab-app`。如果后续发现 App 没有任何可观察的
assistant 消息语义，OPL Cloud verifier 仍先使用消息正文、主内容区域、Processing 状态
和发送控件组合校验；只有发现明确违反 App 已发布 DOM/API 合同的证据时才向 App 仓提交 issue。

## 方案

### 1. Workspace 回复与截图

回复成功必须同时满足：

- marker 出现在主对话内容区域的独立消息正文中；
- 命中的正文不等于用户 prompt，且不位于标题、导航、侧栏、输入框或隐藏节点；
- 页面不再显示活动中的 `Processing`；
- composer 的发送控件恢复可用；
- 成功截图中可以直接看到 assistant marker。

测试夹具必须复现生产截图中的误判：marker 只存在于标题、侧栏和用户消息时，检查失败；
加入真实 assistant 消息并结束 Processing 后才通过。

### 2. Console 页面真实点击 E2E

浏览器使用生产 owner 账号登录 Console，所有商业写操作从页面触发：

```text
登录
-> 打开计算资源创建页
-> 选择 Basic 并确认价格、7 天 Hold、冻结后可用余额
-> 点击开通并等待 running
-> 打开存储创建页并确认价格与 Hold
-> 点击开通并等待 available
-> 页面挂载存储
-> 页面创建 Workspace URL
-> 查看账单中的 compute/storage debit 与冻结金额
-> 页面销毁计算，等待 billing stopped 和 Hold released
-> 页面销毁存储，等待 billing stopped 和 Hold released
```

页面交互优先使用可访问名称：Form label、button name、dialog title、page heading。只有现有
可访问语义不能唯一定位时，才在 OPL Console 自身增加 `data-testid`；不得改 App DOM。

API 在该模式下只允许：

- 读取 Console state、Ledger/Fabric evidence；
- 从 UI 结果解析出的精确 ID 做最终断言；
- 浏览器中断后按 manifest 执行兜底清理。

API 不得替代 Console 页面创建、挂载、Workspace 创建或销毁按钮。

关键状态各保存一张 1280x720 截图：登录后、Hold 预览、5 台之外的单资源 running、
Workspace ready、账单结算、计算销毁后、存储销毁后、Workspace assistant reply。

### 3. 稳定幂等键和资源 manifest

生产 verifier 的每个 mutation 使用稳定键：

```text
production-verification:<run_id>:<resource_slot>:<stage>
```

相同 run、slot、stage 重试必须返回同一商业对象；不同 payload 使用同一键必须冲突。

每次资源创建成功后立即原子写入 run manifest。manifest 至少包含：

- run ID、slot、account ID；
- compute allocation、storage、attachment、Workspace ID；
- Hold ID、Fabric operation ID；
- machine ID、instance ID、node name；
- 创建时的资源名称和幂等键。

任何故障或清理前重新读取生产 state，并要求 account、run ID、名称、资源 ID 与 ownership
全部匹配 manifest。缺字段、重复归属或不一致时 fail closed，只保留 manifest 供人工诊断。

### 4. 5 台腾讯云并发 soak

新增手动 workflow，同时启动 5 个 slot：

```text
<github_run_id>-01 ... <github_run_id>-05
```

每个 slot 创建一套 Basic compute、storage、attachment 和 Workspace。所有 slot 在首台
Machine Ready、ownership active、Workspace ready 后写 ready marker，并在 barrier 等待。
协调器只有在以下条件全部成立时才开始 15 分钟 soak：

- 恰好 5 个 active resource ID；
- 恰好 5 个不同 machine ID、instance ID 和 node name；
- 5 个 Node 都 Ready，标签指向各自 resource ID；
- 5 个 Workspace workload 和 PVC Ready；
- 每个资源只有一个 Hold，首小时只核销一次。

15 分钟内周期性检查 ownership、Node、Pod、PVC、billing status 和 Hold remaining。任一漂移
使 workflow 失败，但仍进入逐 ID cleanup。workflow 设置 60 分钟总超时、同一环境只允许
一个 soak run，并上传 5 份 manifest、JSON、日志和截图。

完整生命周期包含 replacement compute，因此累计最多创建 10 台；运行前容量门要求腾讯云
至少允许 10 台 Basic CVM 的短时创建、50 GB CBS、相应 TKE Node/IP/Pod 配额。容量门不满足
时不发起任何付费写操作。

### 5. 资源级生产故障演练

故障 workflow 逐项执行，前一项完成并清理后才进入下一项：

1. **响应丢失与重放**：服务已接受 mutation 后，客户端丢弃第一次响应；使用同一幂等键
   重放，验证只有一个资源、一个 Hold、一次首小时核销。
2. **Workspace Pod 删除**：仅删除 manifest 指向的 Workspace Pod；验证 Deployment 重建、
   PVC 文件仍可读取、资源仍计费 active，随后正常销毁。
3. **存储 detach/reattach**：仅操作 manifest attachment；验证 detach 后不误删 storage，
   reattach 后同一 PVC 数据可读。
4. **Machine 外部删除**：只有 machine/instance/node 三重 ownership 完全匹配时才删除该
   verifier Machine；验证 provider reconcile 记录 missing/external_deleted，资源停止计费，
   同一 Hold 剩余金额释放，且 allocator 不把该 Machine 归给其他资源。
5. **浏览器或轮询失败**：在资源 Ready 后主动让 verifier 浏览器阶段失败，验证 manifest
   驱动的 catch/finally cleanup 完成且错误不被 cleanup 结果覆盖。

禁止执行：重启或删除共享 Control Plane、Fabric、Ledger；干扰 PostgreSQL；修改共享
Service、Ingress、Gateway、TLS、Secret；整池扩缩或范围删除；操作非 verifier Node；修改
VPC、SG、CAM、DNS；全局停用 reconcile/billing worker；制造腾讯云账户级限流或配额耗尽。

Ledger 整体不可用、数据库锁死和共享服务断网只允许在隔离 canary namespace 演练，不属于
本次共享生产故障 workflow。

### 6. 清理与失败语义

清理顺序固定为 Workspace access、attachment、compute、storage，并逐步等待终态。每一步
都重新校验 manifest ownership，使用稳定 cleanup key。清理成功必须证明：

- 所有 run-owned ownership 为 released；
- 无 run-labeled CVM、Node、Deployment、Pod、Service、Secret、PVC；
- attachment detached，storage destroyed；
- compute/storage billing status 均为 stopped；
- 每个原 Hold 已 released，钱包 balance 未因 release 增加；
- `cleanupErrors` 为空。

任何 cleanup error 都使 workflow 为红，并上传 manifest。禁止用 Node Pool 范围清理脚本
掩盖失败，也禁止在归属不明确时删除资源。

## 测试与发布顺序

实现严格采用 TDD：先让误判、UI 点击合同、幂等重放、5-slot barrier、ownership guard 和
故障清理测试失败，再写最小实现。合并前运行定向 Node tests、183+ 全量 tests、typecheck、
build 和 diff check，并进行代码审查。

生产顺序固定为：

1. push verifier/workflow；
2. 跑现有单链，人工检查全部截图；
3. 跑 5 台并发 soak，确认 15 分钟 barrier 和最终清理；
4. 跑资源级故障 workflow；
5. 再跑诊断，确认无 run 残留；
6. 清理临时 worktree、分支和本地 artifact。

只有三组 workflow 均成功、截图可见、清理为零残留，才能声明本轮生产浏览器与韧性验收闭合。
