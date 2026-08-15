<p align="center">
  <img src="assets/branding/opl-cloud-logo.png" alt="OPL Cloud logo" width="132" />
</p>

<p align="center">
  <a href="./README.md">English</a> | <a href="./README.zh-CN.md"><strong>中文</strong></a>
</p>

<h1 align="center">OPL Cloud</h1>

<p align="center"><strong>让 One Person Lab 的复杂工作在云端连续推进</strong></p>
<p align="center">AI 接入 · 在线工作台 · Agent 服务 · 受控资源 · 证据连续性</p>

<!--
Owner: `one-person-lab-cloud`
Purpose: `public_cloud_entry`
State: `active_public_entry`
Machine boundary: 面向人的产品与架构入口。本仓同时持有 Cloud 实现，但文档、代码、测试或构建本身不证明已部署服务状态、账单真相、发布状态、领域结论或负责人验收。
-->

<p align="center">
  <img src="assets/branding/opl-cloud-overview-v2.png" alt="OPL Cloud 让工作从本机延伸到在线继续、私有数据、远端计算、共同审阅和按需发布" width="100%" />
</p>

## 为什么需要 OPL Cloud

科研、基金、汇报、书籍和 Agent 开发很少在一次会话或一台机器上完成。工作可能从本机开始，随后需要私有数据、远端计算和人工审阅，最后还可能变成可供他人调用的服务。如果这些环节散落在彼此无关的工具中，项目状态、权限、成本和证据就会逐渐脱节。

OPL Cloud 定义如何让这些需求留在同一条 OPL 工作链中：

- 从本机 OPL App 项目自然延续到在线 OPL Workspace；
- 在不转移 owner 权威的前提下，使用获准的模型、数据源、软件环境、存储和计算；
- 把经过验证的精确 Agent Revision 发布为稳定 API、嵌入组件或托管界面；
- 让批准、用量、来源、审阅和继续入口始终连接到原工作；
- 把专业判断留给对应领域 Agent 和人类负责人。

OPL Cloud 是 OPL 稳定生态中的可选第四个产品层：`OPL Base` 提供 Framework Host，
`OPL App` 提供本地工作台，`OPL Packages` 提供可安装能力，Cloud 再增加在线 Workspace、
账号治理、托管资源、协作和 Agent 服务。Cloud 只消费这些 owner 的引用，不替代 Base、
发布 Packages，也不创建第二个 Cordis Host。

## 产品模型

| 用户需要 | 目标产品面 | 责任边界 |
| --- | --- | --- |
| AI 接入与用量 | **OPL Gateway** | 模型接入、路由、provider 策略和用量信号 |
| 在线项目工作 | **OPL Workspace** | 每个账号零个或多个相互独立的在线工作台 |
| Agent 对外使用 | **OPL Serve** | 精确 Service、不可变 Revision、Deployment、API、Embed 和 Hosted UI |
| 账号治理 | **OPL Console** | 账号策略、批准、额度、账单和纳管资源策略 |
| 数据、工具与计算 | **OPL Fabric** | Connect、Compute、Storage、Environments 和执行适配器 |
| 证据连续性 | **OPL Ledger** | 回执、来源、审阅和继续引用 |

Package owner 持有稳定 identity、capabilities、entrypoints 与精确发布 revision；配置的
原生 carrier 持有物理安装、更新、移除与 fresh installed/callable readback。OPL
Framework 只聚合发现、carrier 委托、Package 状态与通用执行语义；OPL Runway 持有
Invocation 与 Session 执行生命周期；领域 Agent 持有专业策略、质量结论、产物和交付
权威。Cloud 各产品面只消费 owner 与 carrier 引用，不创建竞争真相。

## MVP 聚焦

第一期产品只做一条克制的纵向链路：极薄 Console 管理必要的 Workspace、余额与用量；
通过 `Console -> Control Plane -> Workspace launcher/provider -> local Docker`
真实创建和管理 OPL App/WebUI Workspace；通过 Sub2API 读取和结算 Gateway 权威账目，
不建第二钱包。自助开户、充值/支付和精细 UI 均后置。

仓内目前没有 `local-docker` Workspace provider。通用 Compose 资产只能启动 PostgreSQL
和三个 Cloud control services，不能创建、读回或删除 OPL Workspace。当前实现事实以
[状态](docs/status.md)为准，唯一 P0 gap 与优先级以[路线图](docs/roadmap.md)为准。

## 一条连续工作链

```text
本机 OPL App 项目
-> 按需进入在线 OPL Workspace
-> 使用获准的 Gateway / Fabric 能力
-> 结果与审阅回到工作台
-> Ledger refs 保留复查和继续线索
-> 按需通过 OPL Serve 发布精确 Agent Revision
```

每个用户账号可以拥有零个或多个相互独立的 OPL Workspace。每个 Workspace 都有独立的稳定 identity、URL、runtime、资源绑定、账期、凭据和回执。OPL Cloud 在产品层不设置固定数量上限；每次创建仍受余额、provider 容量、额度与策略约束。一个账号也可以发布多个 Agent Service，因为 Service 是部署资源，不是 Workspace。

## 当前仓库边界

`one-person-lab-cloud` 是 OPL Cloud 唯一产品与实现仓，持有公开愿景、目标架构、白皮书、路线图、Console、Control Plane、Fabric、Ledger、Workspace 交付、机器合同、通用安装资产、GHCR 镜像、GitHub Release 与可复用 provider adapter。`opl-cloud` 只作为 npm、镜像、二进制、服务、namespace、环境变量和 runner label 的短标识，不再代表第二个仓库。

`opl-instance-medopl` 是 `medopl` 实例的唯一 owner，持有域名、Tencent/TKE 选择、启用套餐与价格、production environment 与 Secrets、部署 workflow、镜像 pin、回滚和回执。它只消费不可变 Cloud 产品 SHA 与镜像 digest，不复制产品或 runtime 代码。设计存在、合同存在、生成物完成、测试通过或镜像发布，都不代表某个实例已经部署或 ready。

某项能力当前是否可用、运行是否健康、安全与账单是否成立、能否发布以及 owner 是否验收，必须读取对应实现、机器合同、运行输出和负责人回执。[路线图](docs/roadmap.md) 是 Cloud 剩余差距的唯一当前规划 owner，不是 readiness dashboard。

## 从这里开始

- [在线阅读 OPL Cloud 白皮书](https://gaofeng21cn.github.io/one-person-lab-cloud/latest/whitepapers/opl-cloud-whitepaper.html)
- [文档索引与 owner 映射](docs/README.md)
- [架构与权威边界](docs/architecture.md)
- [当前实现能力](docs/status.md)
- [安装独立 OPL Cloud 应用](docs/installation.md)
- [当前路线图、差距和下一轮 Agent Prompt](docs/roadmap.md)
- [Workspace 身份与外部 SaaS 边界](docs/workspace-identity-and-external-saas-boundary.md)

<details>
  <summary><strong>开发者与运维细节</strong></summary>

### 仓库结构

```text
one-person-lab-cloud/
  apps/                Console 用户界面
  assets/              公开品牌与用户旅程资产
  contracts/           白皮书 artifact Profile
  deploy/              通用安装资产和可复用 adapter 模板
  docs/                产品、实现架构、规划与 provenance 文档
  packages/contracts/  当前机器合同
  scripts/             白皮书构建与发布请求 wrapper
  services/            Control Plane、Fabric 与 Ledger
  tools/               本地、产品发布和可复用验证工具
```

技术文档统一从 [docs/README.md](docs/README.md) 进入。产品目标、当前实现、实例配置和外部 owner truth 必须保持可区分，不得建立第二个 Cloud writer。

### 参与开发

提交 Pull Request 前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。`main` 由
严格的 `validate` 汇总检查和已解决的 review 对话保护；生产与部署结论仍必须
通过各自独立的授权与证据门禁。

### 最小检查

```bash
node --experimental-strip-types scripts/build-opl-cloud-whitepaper.ts
npm test
npm run typecheck
npm run build
git diff --check
```

白皮书构建只证明 artifact 渲染；发布还必须经过批准工作流和公开 exact-byte 回读，边界见 [白皮书交付证据](docs/delivery/whitepapers/README.md)。

</details>
