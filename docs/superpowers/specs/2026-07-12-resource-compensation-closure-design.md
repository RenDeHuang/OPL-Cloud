# OPL Cloud 资源补偿闭环设计

## 状态

方案于 2026-07-12 确认。本文只修复资源冻结释放、批量结算隔离和 Workspace 创建补偿，不改变现有产品流程、价格模型或服务边界。

## 问题

当前正常路径可以完成资源开通和计费，但有三个失败路径没有闭环：

1. Fabric 异步计算开通失败后，Control Plane 不会自动释放 Ledger 冻结。
2. 周期结算遇到一个资源失败会停止本轮，后续资源无法结算。
3. Fabric 已创建 Workspace Runtime、Ledger receipt 写入失败时，会残留云端 Runtime。

另一个共同根因会影响正常销毁：Fabric operation 回写资源状态时替换了 Control Plane 资源记录。Fabric 不拥有 `holdId`、`holdAmountCents`、价格快照和用户命名等商业字段，这些字段丢失后，计算或存储销毁无法向 Ledger 提交释放请求。

## 设计原则

- Control Plane 拥有账户、用户、价格、冻结和计费投影。
- Fabric 拥有 provider 状态、provider 标识和云操作证据。
- Ledger 是冻结、释放、扣款和钱包余额的资金权威。
- 所有补偿操作使用稳定幂等键；重试不得重复释放资金或重复删除资源。
- 复用现有 worker、operation store 和 HTTP client，不新增队列、回调或数据库表。

## 资源投影合并

Control Plane 消费 Fabric terminal operation 时，以现有 Control Plane 记录为基础合并 Fabric 资源结果。Fabric 结果可以更新状态和 provider 字段，但不得清除现有记录中的：

- `accountId`、`ownerAccountId`、`ownerUserId` 和用户名称；
- `holdId`、`holdAmountCents` 和 `holdReleaseId`；
- `pricingVersion`、`priceSnapshot` 和 billing settlement 引用；
- 已有关联的 Workspace 和资源关系字段。

合并后的完整记录通过现有 `SaveCompute`、`SaveStorage` 和 `SaveAttachment` 保存。

## 冻结释放

Control Plane 提供一个内部资源冻结释放操作，包装现有 Ledger `ReleaseHold`：

- 输入使用资源记录中持久化的 account、workspace、resource、hold 和 amount；
- 幂等键由终态原因、资源类型和资源 ID 稳定生成；
- 已有 `holdReleaseId` 或 billing 已停止时跳过；
- 成功后保存 `holdReleaseId`、钱包和 `billingStatus=stopped`。

以下终态共用该操作：

- 计算异步创建 operation 变为 `failed`；
- provider 同步发现资源被外部删除；
- 用户显式销毁计算或存储资源。

显式销毁仍由现有请求同步释放。Provider reconcile worker 负责发现异步失败并补偿，保证不依赖用户打开页面或手动同步。

## 结算隔离

周期结算继续逐资源调用 Ledger。单个资源失败时：

1. 保存或记录该资源的失败信息；
2. 继续处理本轮其他资源；
3. 本轮结束后使用组合错误返回全部失败，使 worker 日志和监控仍能发现异常。

成功资源照常保存 settlement 和钱包投影。一个账户余额不足不得阻止其他账户结算。

## Workspace Runtime 补偿

Fabric 增加内部、幂等的 Workspace Runtime 删除操作。它清理该 Workspace 对应的 Deployment、Service 和 Secret；共享 Ingress 由 Control Plane 网关按 Workspace 路由，不属于单个 Runtime，补偿不得修改它。资源已不存在视为成功，并记录 Fabric operation 证据。

Control Plane 的 Workspace 创建顺序保持：

1. Fabric 创建 Runtime；
2. Ledger 写入 receipt；
3. Control Plane 返回并保存 Workspace 投影。

第 2 步失败时，Control Plane 立即调用 Fabric 删除 Runtime。返回错误保留 Ledger 原始错误；如果补偿也失败，使用组合错误同时返回两者，便于后续运维定位残留资源。没有成功 receipt 时不得返回成功 Workspace。

## 测试

使用现有 Go 测试设施，以失败测试先行覆盖：

- Fabric operation 回写后，计算和存储仍保留 hold、pricing 和 ownership 字段；
- operation 变为异步创建失败后，worker 只释放一次冻结并保存释放结果；
- 计算和存储经过 operation 同步后再销毁，Ledger 收到原始 hold ID 和金额；
- 第一个资源结算失败时，后续资源仍会结算，最终返回组合错误；
- receipt 写入失败时调用 Runtime 删除；删除失败时两个错误都可观察；
- Runtime 删除重复调用成功且不会泄露 Workspace 凭据。

完成后运行三个 Go 服务测试、Node 合约测试、TypeScript 类型检查、生产构建和 `git diff --check`。真实云 E2E 会产生费用，不在本次自动执行范围内。

## 非目标

- 不改为同步等待 CVM 创建。
- 不让 Fabric 反向调用 Control Plane。
- 不引入分布式事务、消息队列或 outbox 表。
- 不改变七天冻结和每小时结算价格模型。
- 不处理外部支付结算、GPU 或 Workspace snapshot 生产约束。
