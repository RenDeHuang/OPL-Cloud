# OPL Console Local Demo Design

状态：`approved`

## 目标

为 React Console 提供一个可直接在普通浏览器登录和点击的本地演示入口，同时保持生产运行时、Control Plane、计费和云资源边界不变。

## 设计

- 新增独立 `npm run demo` 命令，只监听 `127.0.0.1`。
- 使用 Vite middleware 提供现有 React Console，并在同一 localhost origin 下响应 `/api/*`。
- Demo 启动时显式禁用 Vite API proxy；所有 `/api` 前缀或编码变体都在本地 fail closed，不能落入 SPA 或外部代理。
- 复用 `tools/console-browser-qa.ts` 的 fake-only fixture 和 DTO，不在产品代码中加入 demo 参数、测试账号或浏览器端业务真相。
- 提供客户和 Admin 两套固定演示凭据；凭据只由启动命令输出。
- 登录签发 localhost-only、HttpOnly、SameSite Session Cookie；每次登录都轮换服务端随机 Session ID，客户端提供的未知 ID 不进入 Session 存储；服务端按 Session 隔离角色和账户，退出只清理当前客户端。
- 客户 Workspace、Key、账单、Support 和 Secret 读取都按当前 Session 的 `accountId` 过滤；动态 General Key 与 Workspace Key 使用全局唯一 ID，owner 和 Secret 保留在内部 Key 记录且不投影给浏览器；Admin 权限不授予其他账户的客户 Secret。
- 人工演示关闭 Browser QA 专用的首次响应丢失注入，保留幂等键、SourceEnvelope、Secret reveal 和角色门禁语义。
- 演示服务不访问外网，不连接 Control Plane、Sub2API、Fabric、Ledger 或腾讯云，也不产生真实计费或资源副作用。
- 关闭流程先停止接受连接，再在有界宽限期后回收半开连接并关闭 Vite，避免残留端口或进程。

## 验收

- 未登录访问 `/api/auth/me` 返回 `401`。
- 客户账号登录后进入 `/console/overview`，可以浏览客户五个一级页面。
- Admin 账号登录后同时拥有客户和 Admin 导航，可以打开 `/admin/overview`。
- 客户、Admin 和匿名客户端的 Session 相互隔离；客户退出不影响 Admin Session。
- 预置客户端 Session ID 后登录会强制轮换，持有旧 ID 的客户端仍为未登录。
- 不同账户创建的 General Key 和 Workspace Key 拥有不同 ID 与 Secret，任一交叉 reveal 都返回 `404`。
- Admin 访问客户账户的 Workspace Secret 返回 `404`。
- `/api`、`/apiX` 和编码 API 路径均由本地服务返回 `404`，外部代理命中数为零。
- 关键 fixture 写操作只修改进程内状态。
- 半开请求存在时，服务仍在有界时间内关闭且不遗留端口或子进程。

## ImageGen 边界

ImageGen 用于生成静态视觉方向、状态稿或位图资产，不能实现路由、表单、焦点、状态机和 API 交互。本次补丁是运行入口修复，不需要新增位图资产；交互继续由 React 与 `@openai/apps-sdk-ui` 实现。
